/*
PS4 Media Browser
MobCat - 2025

A Simple tool for browser and download screenshots and clips from your hacked PS4
as we can't use PSN for this, we needed another way to share screenhots and clips.

This tool will connect to your PS4 running GoldHEN and ftp server.
You will need to note down the ip that the ps4 tells you so you can add it to your config.ini

TODO: This is a functing project, but its more of a PoC
It works, but it contains some hackey work arounds I would like to solve at some point TM.
Mainly the whole using a static json object rather then /system_dasta/priv/mms/app.db
*/

package main

import (
    "encoding/json"
    "fmt"
    "io"
    "log"
    "net/http"
    "os"
    "os/exec"
    "path/filepath"
    "regexp"
    "runtime"
    "strings"
    "sync"
    "time"

    "github.com/jlaffaye/ftp"
    "gopkg.in/ini.v1"
)

// Config for FTP server configuration
type Config struct {
    Host     string
    Port     int
    Username string
    Password string
}

// PS4TitleInfo information about a PS4 game title
//TODO: This json file is a hack.
type PS4TitleInfo struct {
    ContentID   string `json:"Content_ID"`
    NameDefault string `json:"name_default"`
    Icon0       string `json:"icon0"` // Placeholder. We may use this lator as a nicer way to brows your games rather then a text list.
}

// Content thumbnails and its associated media file
type MediaItem struct {
    TitleID      string    `json:"title_id"`
    GameTitle    string    `json:"game_title"`
    Type         string    `json:"type"` // "photo" or "video"
    ThumbnailURL string    `json:"thumbnail_url"`
    MediaURL     string    `json:"media_url"`
    Filename     string    `json:"filename"`
    Folder       string    `json:"folder"`
    Date         time.Time `json:"date"`
    DateStr      string    `json:"date_str"`
}

// FTP connection pool
type FTPPool struct {
    mu      sync.Mutex
    conns   []*ftp.ServerConn
    config  Config
    maxSize int
}

func NewFTPPool(config Config, maxSize int) *FTPPool {
    return &FTPPool{
        config:  config,
        maxSize: maxSize,
    }
}

func (p *FTPPool) Get() (*ftp.ServerConn, error) {
    p.mu.Lock()
    defer p.mu.Unlock()

    // Try to get existing connection
    if len(p.conns) > 0 {
        conn := p.conns[len(p.conns)-1]
        p.conns = p.conns[:len(p.conns)-1]

        // Test if connection is still alive
        if err := conn.NoOp(); err == nil {
            return conn, nil
        }
        // Connection is dead, close it
        conn.Quit()
    }

    // Create new connection
    return p.createConnection()
}

func (p *FTPPool) Put(conn *ftp.ServerConn) {
    if conn == nil {
        return
    }

    p.mu.Lock()
    defer p.mu.Unlock()

    // Test if connection is still alive
    if err := conn.NoOp(); err != nil {
        conn.Quit()
        return
    }

    // Return to pool if not full
    if len(p.conns) < p.maxSize {
        p.conns = append(p.conns, conn)
    } else {
        conn.Quit()
    }
}

func (p *FTPPool) createConnection() (*ftp.ServerConn, error) {
    conn, err := ftp.Dial(fmt.Sprintf("%s:%d", p.config.Host, p.config.Port),
        ftp.DialWithTimeout(time.Duration(30)*time.Second))
    if err != nil {
        return nil, err
    }

    err = conn.Login(p.config.Username, p.config.Password)
    if err != nil {
        conn.Quit()
        return nil, err
    }

    return conn, nil
}

func (p *FTPPool) Close() {
    p.mu.Lock()
    defer p.mu.Unlock()

    for _, conn := range p.conns {
        conn.Quit()
    }
    p.conns = nil
}

var (
    config        Config
    ftpPool       *FTPPool
    ps4TitleNames map[string]PS4TitleInfo
)

func main() {
    // Load configuration
    if err := loadConfig(); err != nil {
        log.Fatal("Error loading config:", err)
    }

    // Load PS4 title names
    // Initialize ps4TitleNames as an empty map first, to prevent nil panics
    // This is a dumb hack. we can load all active titles from
    // /system_dasta/priv/mms/app.db
    // However getting sqlite dbs loading in go seems to be more of a pain then it's worth..
    // There should be a default lib for it though?
    log.Println("Loading ps4Title.json")
    ps4TitleNames = make(map[string]PS4TitleInfo)
    if err := loadPS4TitleNames("ps4Title.json"); err != nil {
        log.Println("Warning: Could not load ps4Title.json. Game titles will be displayed as Title IDs.\nError:", err)
    }

    // Create FTP connection pool
    log.Println("Connecting to", config)
    ftpPool = NewFTPPool(config, 10) // Max 10 concurrent connections
    defer ftpPool.Close()

    // Test initial connection
    testConn, err := ftpPool.Get()
    if err != nil {
        log.Fatal("Error connecting to FTP. Check if the console is on and the ftp server is running.\n", err)
        fmt.Print("Press 'Enter' to exit...")
        fmt.Scanln()
    }
    ftpPool.Put(testConn)

    // Setup HTTP routes
    http.HandleFunc("/", homeHandler)
    http.HandleFunc("/api/thumbnails", thumbnailsHandler)
    http.HandleFunc("/media/", mediaHandler)
    http.HandleFunc("/download/", downloadHandler)
    http.HandleFunc("/static/", staticHandler) // This will still return 404 as no static files are served

    log.Println("Server starting on localhost:8080")

    // Auto-open browser
    go func() {
        time.Sleep(1 * time.Second) // Give server time to start
        openBrowser("http://localhost:8080")
    }()

    log.Fatal(http.ListenAndServe(":8080", nil))
}

func loadConfig() error {
    // Create default config file if it doesn't exist
    configFile := "config.ini"
    if _, err := os.Stat(configFile); os.IsNotExist(err) {
        if err := createDefaultConfig(configFile); err != nil {
            return err
        }
        fmt.Println("Created default config.ini file. Please update it with your FTP settings.")
    }

    cfg, err := ini.Load(configFile)
    if err != nil {
        return err
    }

    section := cfg.Section("ftp")
    config = Config{
        Host:     section.Key("host").String(),
        Port:     section.Key("port").MustInt(21),
        Username: section.Key("username").String(),
        Password: section.Key("password").String(),
    }

    return nil
}

func createDefaultConfig(filename string) error {
    content := `[ftp]
host = YourPS4IPHere
port = 2121
username = anonymous
password = anonymous
`
    return os.WriteFile(filename, []byte(content), 0644)
}

func loadPS4TitleNames(filename string) error {
    file, err := os.ReadFile(filename)
    if err != nil {
        return fmt.Errorf("failed to read %s: %w", filename, err)
    }

    err = json.Unmarshal(file, &ps4TitleNames)
    if err != nil {
        return fmt.Errorf("failed to unmarshal %s: %w", filename, err)
    }

    return nil
}

func getGameTitle(titleID string) string {
    if info, ok := ps4TitleNames[titleID]; ok {
        return info.NameDefault
    }
    return titleID // Return title ID if not found
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/html")

    // This is the blob of html data that is served to the browser as our "UI" for this app.
    fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
    <title>PS4 Media Browser</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 20px; background: #1a1a1a; color: #e0e0e0; }
        .header { background: #2d2d2d; padding: 20px; border-radius: 8px; margin-bottom: 20px; box-shadow: 0 2px 4px rgba(0,0,0,0.3); }
        .filters { margin-bottom: 20px; }
        .filter-group { display: inline-block; margin-right: 20px; }
        select, input { padding: 8px; margin: 5px; border: 1px solid #555; border-radius: 4px; background: #3a3a3a; color: #e0e0e0; }
        select:focus, input:focus { outline: none; border-color: #4a9eff; }
        .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 20px; }
        .item { background: #2d2d2d; border-radius: 8px; overflow: hidden; box-shadow: 0 2px 8px rgba(0,0,0,0.3); transition: transform 0.2s; }
        .item:hover { transform: translateY(-2px); box-shadow: 0 4px 12px rgba(0,0,0,0.4); }
        .thumbnail { width: 100%%; height: 200px; object-fit: cover; cursor: pointer; }
        .thumbnail-container { position: relative; }
        .play-icon { position: absolute; top: 50%%; left: 50%%; transform: translate(-50%%, -50%%); width: 60px; height: 60px; background: rgba(0,0,0,0.8); border-radius: 50%%; display: flex; align-items: center; justify-content: center; pointer-events: none; }
        .play-icon svg { width: 30px; height: 30px; fill: #e0e0e0; margin-left: 3px; }
        .info { padding: 15px; }
        .game-title { font-weight: bold; color: #f0f0f0; margin-bottom: 5px; }
        .title-id { color: #ccc; font-size: 0.9em; margin-bottom: 5px; }
        .filename { color: #b0b0b0; font-size: 0.9em; margin-bottom: 5px; }
        .date { color: #888; font-size: 0.8em; }
        .loading { text-align: center; padding: 50px; color: #b0b0b0; }
        .progress { margin-top: 10px; color: #b0b0b0; font-size: 0.9em; }
        .modal { display: none; position: fixed; z-index: 1000; left: 0; top: 0; width: 100%%; height: 100%%; background: rgba(0,0,0,0.9); }
        .modal-content { position: relative; margin: 5%% auto; width: 90%%; max-width: 70%%; background: #2d2d2d; border-radius: 8px; }
        .modal-body { padding: 10px; } /* Added padding for modal content */
        .modal img, .modal video { width: 100%%; height: auto; display: block; }
        .modal-footer {
            padding: 15px;
            display: flex; /* Use flexbox for layout */
            justify-content: space-between; /* Space items out */
            align-items: center; /* Vertically align items */
            border-top: 1px solid #3a3a3a;
        }
        .modal-info {
            text-align: left;
            color: #b0b0b0;
            font-size: 0.9em;
        }
        .modal-info .game-title { font-weight: bold; color: #f0f0f0; margin-bottom: 3px; }
        .modal-info .title-id, .modal-info .filename, .modal-info .date { font-size: 0.85em; margin-bottom: 2px; }

        .modal-actions {
            text-align: right;
            white-space: nowrap; /* Prevent buttons from wrapping */
        }
        .close { position: absolute; right: 15px; top: 10px; font-size: 28px; font-weight: bold; color: #b0b0b0; cursor: pointer; z-index: 1001; }
        .close:hover { color: #f0f0f0; }
        .btn { display: inline-block; padding: 8px 16px; background: #2196f3; color: white; text-decoration: none; border-radius: 4px; font-size: 0.9em; margin-left: 10px; cursor: pointer; border: none;}
        .btn:hover { background: #1976d2; }
        .btn.download { background: #4caf50; }
        .btn.download:hover { background: #388e3c; }
        .btn.copy { background: #ff9800; }
        .btn.copy:hover { background: #e68a00; }
        .lazy-load { opacity: 0; transition: opacity 0.3s; }
        .lazy-load.loaded { opacity: 1; }
    </style>
</head>
<body>
    <div class="header">
            <div style="margin-bottom: 30px;">
        <div style="display: flex; align-items: center; gap: 10px;">
            <svg xmlns="http://www.w3.org/2000/svg" height="64" width="64" viewBox="0 0 48 32"><path fill="#0070d1" d="M.81 22.6c-1.5 1-1 2.9 2.2 3.8 3.3 1.1 6.9 1.4 10.4.8.2 0 .4-.1.5-.1v-3.4l-3.4 1.1c-1.3.4-2.6.5-3.9.2-1-.3-.8-.9.4-1.4l6.9-2.4v-3.7l-9.6 3.3c-1.2.4-2.4 1-3.5 1.8zm23.2-15v9.7c4.1 2 7.3 0 7.3-5.2 0-5.3-1.9-7.7-7.4-9.6-2.9-1-5.9-1.9-8.9-2.5v28.9l7 2.1V6.7c0-1.1 0-1.9.8-1.6 1.1.3 1.2 1.4 1.2 2.5zm13 12.7c-2.9-1-6-1.4-9-1.1-1.6.1-3.1.5-4.5 1l-.3.1v3.9l6.5-2.4c1.3-.4 2.6-.5 3.9-.2 1 .3.8.9-.4 1.4l-10 3.7v3.8l13.8-5.1c1-.4 1.9-.9 2.7-1.7.7-1 .4-2.4-2.7-3.4z"/></svg>
            <h1 style="margin: 0;">PS4 Media Browser</h1>
        </div>
        </div>
        <div class="filters">
            <div class="filter-group">
                <label>Game Title:</label>
                <select id="titleFilter">
                    <option value="">All Titles</option>
                </select>
            </div>
            <div class="filter-group">
                <label>Type:</label>
                <select id="typeFilter">
                    <option value="">All Types</option>
                    <option value="photo">Photos</option>
                    <option value="video">Videos</option>
                </select>
            </div>
            <div class="filter-group">
                <label>Sort by:</label>
                <select id="sortFilter">
                    <option value="date_desc">Date (Newest First)</option>
                    <option value="date_asc">Date (Oldest First)</option>
                    <option value="game_title_asc">Game Title (A-Z)</option>
                    <option value="game_title_desc">Game Title (Z-A)</option>
                </select>
            </div>
        </div>
    </div>

    <div id="loading" class="loading">
        <div>Loading content...<br>Please wait, this may take some time...</div>
    </div>
    <div id="grid" class="grid" style="display: none;"></div>

    <div id="modal" class="modal">
        <div class="modal-content">
            <span class="close">&times;</span>
            <div id="modalMedia" class="modal-body"></div>
            <div class="modal-footer">
                <div id="modalInfo" class="modal-info">
                    <!-- Media info will be injected here by JavaScript -->
                </div>
                <div class="modal-actions">
                    <button id="copyBtn" class="btn copy">Copy to Clipboard</button>
                    <a id="downloadBtn" class="btn download" href="#" download>Download</a>
                </div>
            </div>
        </div>
    </div>

    <script>
        let allItems = [];
        let filteredItems = [];
        let loadingProgress = { loaded: 0, total: 0 };
        let currentMediaItem = null; // Store the entire item for modal info

        async function loadThumbnails() {
            try {
                const response = await fetch('/api/thumbnails');
                allItems = await response.json();
                loadingProgress.total = allItems.length;
                
                // Reset loaded counter for actual image loading progress
                loadingProgress.loaded = 0; 
                
                updateFilters();
                filterAndDisplay();
                document.getElementById('loading').style.display = 'none';
                document.getElementById('grid').style.display = 'grid';
            } catch (error) {
                console.error('Error loading thumbnails:', error);
                document.getElementById('loading').innerHTML = '<div>Error loading thumbnails: ' + error.message + '</div>';
            }
        }


        function updateFilters() {
            const titleFilter = document.getElementById('titleFilter');
            // Use game_title for filters, ensuring unique titles
            const titles = [...new Set(allItems.map(item => item.game_title))].sort((a, b) => {
                // Heuristic to put Title IDs at the end of the sort order for filters
                const isATitleID = a.startsWith('CUSA') || a.startsWith('EP') || a.startsWith('UP');
                const isBTitleID = b.startsWith('CUSA') || b.startsWith('EP') || b.startsWith('UP');

                if (isATitleID && !isBTitleID) return 1; // A is Title ID, B is not -> A comes after B
                if (!isATitleID && isBTitleID) return -1; // B is Title ID, A is not -> B comes after A
                return a.localeCompare(b); // Both are Title IDs or both are names, sort alphabetically
            });
            
            titleFilter.innerHTML = '<option value="">All Titles</option>';
            titles.forEach(title => {
                const option = document.createElement('option');
                option.value = title; // Filter by game_title directly
                option.textContent = title;
                titleFilter.appendChild(option);
            });
        }

        function filterAndDisplay() {
            const titleFilter = document.getElementById('titleFilter').value;
            const typeFilter = document.getElementById('typeFilter').value;
            const sortFilter = document.getElementById('sortFilter').value;

            filteredItems = allItems.filter(item => {
                return (!titleFilter || item.game_title === titleFilter) && // Filter by game_title
                       (!typeFilter || item.type === typeFilter);
            });

            // Sort items
            filteredItems.sort((a, b) => {
                switch (sortFilter) {
                    case 'date_desc':
                        return new Date(b.date) - new Date(a.date);
                    case 'date_asc':
                        return new Date(a.date) - new Date(b.date);
                    case 'game_title_asc': // Sort by game_title
                        return a.game_title.localeCompare(b.game_title);
                    case 'game_title_desc': // Sort by game_title
                        return b.game_title.localeCompare(a.game_title);
                    default:
                        return new Date(b.date) - new Date(a.date);
                }
            });

            displayItems();
        }

        function displayItems() {
            const grid = document.getElementById('grid');
            grid.innerHTML = '';
            

            filteredItems.forEach((item, index) => {
                const div = document.createElement('div');
                div.className = 'item';
                
                // Create thumbnail container
                const thumbnailContainer = document.createElement('div');
                thumbnailContainer.className = 'thumbnail-container';
                
                // Create image element with lazy loading
                const img = document.createElement('img');
                img.className = 'thumbnail lazy-load';
                img.alt = 'Thumbnail';
                img.onclick = () => openModal(item); // Pass the entire item object
                
                // Add error handling for failed image loads
                img.onerror = function() {
                    this.src = 'data:image/svg+xml;base64,PHN2ZyB3aWR0aD0iMzAwIiBoZWlnaHQ9IjIwMCIgeG1sbnM9Imh0dHA6Ly93d3cudzMub3JnLzIwMDAvc3ZnIj48cmVjdCB3aWR0aD0iMTAwJSIgaGVpZ2h0PSIxMDAlIiBmaWxsPSIjZGRkIi8+PHRleHQgeD0iNTAlIiB5PSI1MCUiIGZvbnQtZmFtaWx5PSJBcmlhbCIgZm9udC1zaXplPSIxNCIgZmlsbD0iIzk5OSIgdGV4dC1hbmNob3I9Im1pZGRsZSIgZHk9Ii4zZW0iPkZhaWxlZCB0byBsb2FkPC90eHQ+PC9zdmc+';
                    this.classList.add('loaded');
                    loadingProgress.loaded++;
                };
                
                img.onload = function() {
                    this.classList.add('loaded');
                    loadingProgress.loaded++;
                };

                thumbnailContainer.appendChild(img);
                
                // Add play icon for videos
                if (item.type === 'video') {
                    const playIcon = document.createElement('div');
                    playIcon.className = 'play-icon';
                    playIcon.innerHTML = '<svg viewBox="0 0 24 24"><path d="M8 5v14l11-7z"/></svg>';
                    thumbnailContainer.appendChild(playIcon);
                }
                
                // Clean up filename - remove the extra .jpg extension from the thumbnail name for display
                let cleanFilename = item.filename;
                if (cleanFilename.endsWith('.jpg') && (cleanFilename.includes('.jpg.jpg') || cleanFilename.includes('.mp4.jpg'))) {
                    cleanFilename = cleanFilename.slice(0, -4);
                }

                const infoDiv = document.createElement('div');
                infoDiv.className = 'info';
                infoDiv.innerHTML = '<div class="game-title">' + item.game_title + '</div>' + // Display game_title prominently
                    '<div class="title-id">' + item.title_id + '</div>' + // Still show Title ID, but smaller
                    '<div class="filename">' + cleanFilename + '</div>' +
                    '<div class="date">' + item.date_str + '</div>';
                
                div.appendChild(thumbnailContainer);
                div.appendChild(infoDiv);
                
                // Delay loading images to prevent overwhelming the FTP server
                // We're loading thumbnails on the main page, so this needs to be throttled.
                // Use requestAnimationFrame for smoother loading effect if many items,
                // but setTimeout is fine for controlled pacing.
                setTimeout(function() {
                    img.src = '/media/' + encodeURIComponent(item.thumbnail_url);
                }, index * 100); // 100ms delay between each image
                
                grid.appendChild(div);
            });
        }

        function openModal(item) { // Now accepts the entire item object
            const modal = document.getElementById('modal');
            const modalMedia = document.getElementById('modalMedia');
            const modalInfo = document.getElementById('modalInfo');
            const downloadBtn = document.getElementById('downloadBtn');
            const copyBtn = document.getElementById('copyBtn');
            
            currentMediaItem = item; // Store the entire item for modal info

            modalMedia.innerHTML = ''; // Clear previous content
            modalInfo.innerHTML = ''; // Clear previous info

            // Display media
            if (item.type === 'video') {
                modalMedia.innerHTML = '<video controls autoplay>' +
                    '<source src="/media/' + encodeURIComponent(item.media_url) + '" type="video/mp4">' +
                    'Your browser does not support the video tag.' +
                    '</video>';
                copyBtn.style.display = 'none'; // Hide copy button for videos
            } else {
                modalMedia.innerHTML = '<img id="modalImage" src="/media/' + encodeURIComponent(item.media_url) + '" alt="Full size image">';
                copyBtn.style.display = 'inline-block'; // Show copy button for images
            }
            
            // Populate modal info
            let cleanFilename = item.filename;
            if (cleanFilename.endsWith('.jpg') && (cleanFilename.includes('.jpg.jpg') || cleanFilename.includes('.mp4.jpg'))) {
                cleanFilename = cleanFilename.slice(0, -4);
            }
            // Corrected: changed template literal to standard string concatenation
            modalInfo.innerHTML = '<div class="game-title">' + item.game_title + '</div>' +
                                  '<div class="title-id">' + item.title_id + '</div>' +
                                  '<div class="filename">' + cleanFilename + '</div>' +
                                  '<div class="date">' + item.date_str + '</div>';

            // Set download link
            downloadBtn.href = '/download/' + encodeURIComponent(item.media_url);
            modal.style.display = 'block';
        }

        function closeModal() {
            const modal = document.getElementById('modal');
            const modalMedia = document.getElementById('modalMedia');
            
            // Stop any playing videos
            const videos = modalMedia.querySelectorAll('video');
            videos.forEach(video => {
                video.pause();
                video.currentTime = 0;
            });
            
            modal.style.display = 'none';
            modalMedia.innerHTML = '';
            document.getElementById('modalInfo').innerHTML = ''; // Clear modal info
            currentMediaItem = null; // Clear stored item
        }

        async function copyMediaToClipboard() {
            if (!currentMediaItem) {
                alert('No media selected to copy.');
                return;
            }

            if (currentMediaItem.type === 'video') {
                alert('Direct copying of video to clipboard is not widely supported by browsers. Please use the "Download" button or right-click the video and choose "Save Video As..."');
                return;
            }

            // --- Image Copy Logic (using Canvas for broader support) ---
            const img = document.getElementById('modalImage');
            // Ensure the image is fully loaded before attempting to draw it on canvas
            if (!img || !img.naturalWidth || img.naturalWidth === 0) {
                alert('Image not fully loaded or not found in modal. Please wait a moment or try again.');
                return;
            }

            try {
                const canvas = document.createElement('canvas');
                canvas.width = img.naturalWidth;
                canvas.height = img.naturalHeight;
                const ctx = canvas.getContext('2d');
                ctx.drawImage(img, 0, 0);

                canvas.toBlob(async (blob) => {
                    if (blob) {
                        try {
                            const clipboardItem = new ClipboardItem({ 'image/png': blob });
                            await navigator.clipboard.write([clipboardItem]);
                            alert('Image copied to clipboard!');
                        } catch (err) {
                            console.error('Failed to copy image to clipboard (ClipboardItem API):', err);
                            alert('Failed to copy image to clipboard. Error: ' + err.message + '\n\nTry right-clicking the image and selecting "Copy Image".');
                        }
                    } else {
                        alert('Could not convert image to blob for copying.');
                    }
                }, 'image/png'); // Export as PNG for better quality and transparency support

            } catch (err) {
                console.error('Failed to copy image to clipboard (Canvas error):', err);
                alert('Failed to copy image to clipboard. Error: ' + err.message + '\n\nTry right-clicking the image and selecting "Copy Image".');
            }
        }


        // Event listeners
        document.getElementById('titleFilter').addEventListener('change', filterAndDisplay);
        document.getElementById('typeFilter').addEventListener('change', filterAndDisplay);
        document.getElementById('sortFilter').addEventListener('change', filterAndDisplay);

        // Modal close functionality
        document.querySelector('.close').addEventListener('click', closeModal);
        document.getElementById('copyBtn').addEventListener('click', copyMediaToClipboard);

        window.addEventListener('click', (event) => {
            const modal = document.getElementById('modal');
            if (event.target === modal) {
                closeModal();
            }
        });

        // Load thumbnails on page load
        loadThumbnails();
    </script>
</body>
</html>`)
}

func thumbnailsHandler(w http.ResponseWriter, r *http.Request) {
    items, err := scanThumbnails()
    if err != nil {
        http.Error(w, "Error scanning thumbnails: "+err.Error(), http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(items)
}

func scanThumbnails() ([]MediaItem, error) {
    var items []MediaItem
    mediaTypes := []string{"photo", "video"}

    // Get connection from pool
    conn, err := ftpPool.Get()
    if err != nil {
        return nil, err
    }
    defer ftpPool.Put(conn)

    for _, mediaType := range mediaTypes {
        basePath := fmt.Sprintf("/user/av_contents/thumbnails/%s/NPXS20001", mediaType)

        // List title directories
        titleDirs, err := conn.List(basePath)
        if err != nil {
            log.Printf("Error listing %s: %v", basePath, err)
            continue
        }

        for _, titleDir := range titleDirs {
            if titleDir.Type != ftp.EntryTypeFolder {
                continue
            }

            titleID := titleDir.Name
            gameTitle := getGameTitle(titleID) // Get the game title
            titlePath := fmt.Sprintf("%s/%s", basePath, titleID)

            // List folder directories (3-digit codes)
            folderDirs, err := conn.List(titlePath)
            if err != nil {
                log.Printf("Error listing %s: %v", titlePath, err)
                continue
            }

            for _, folderDir := range folderDirs {
                if folderDir.Type != ftp.EntryTypeFolder {
                    continue
                }

                folder := folderDir.Name
                folderPath := fmt.Sprintf("%s/%s", titlePath, folder)

                // List files in folder
                files, err := conn.List(folderPath)
                if err != nil {
                    log.Printf("Error listing %s: %v", folderPath, err)
                    continue
                }

                for _, file := range files {
                    if file.Type == ftp.EntryTypeFolder {
                        continue
                    }

                    // Parse filename to extract date
                    date, err := parseDate(file.Name)
                    if err != nil {
                        log.Printf("Error parsing date from %s: %v", file.Name, err)
                        continue
                    }

                    thumbnailPath := fmt.Sprintf("%s/%s", folderPath, file.Name)
                    mediaPath := getMediaPath(thumbnailPath, mediaType)

                    item := MediaItem{
                        TitleID:      titleID,
                        GameTitle:    gameTitle, // Assign the resolved game title
                        Type:         mediaType,
                        ThumbnailURL: thumbnailPath,
                        MediaURL:     mediaPath,
                        Filename:     file.Name,
                        Folder:       folder,
                        Date:         date,
                        DateStr:      date.Format("2006-01-02 15:04:05"),
                    }

                    items = append(items, item)
                }
            }
        }
    }

    return items, nil
}

func parseDate(filename string) (time.Time, error) {
    // Extract date from filename like "20250604_220432_00414242.jpg.jpg"
    re := regexp.MustCompile(`(\d{8})_(\d{6})`)
    matches := re.FindStringSubmatch(filename)

    if len(matches) < 3 {
        return time.Time{}, fmt.Errorf("could not parse date from filename: %s", filename)
    }

    dateStr := matches[1] + matches[2]
    return time.ParseInLocation("20060102150405", dateStr, time.Local)
}

func getMediaPath(thumbnailPath, mediaType string) string {
    // Convert thumbnail path to media path
    // From: /user/av_contents/thumbnails/photo/NPXS20001/CUSA34697/1dd/20250607_165838_00792372.jpg.jpg
    // To:   /user/av_contents/photo/NPXS20001/CUSA34697/1dd/20250607_165838_00792372.jpg

    mediaPath := strings.Replace(thumbnailPath, "/thumbnails/"+mediaType+"/", "/"+mediaType+"/", 1)

    // Removes .jpg.jpg or .mp4.jpg weirdness with the thumbnail
    if strings.HasSuffix(mediaPath, ".jpg") {
        mediaPath = mediaPath[:len(mediaPath)-4]
    }

    return mediaPath
}

// openBrowser opens the specified URL in the default browser
func openBrowser(url string) {
    var err error
    switch runtime.GOOS {
    case "linux":
        err = exec.Command("xdg-open", url).Start()
    case "windows":
        err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
    case "darwin":
        err = exec.Command("open", url).Start()
    default:
        err = fmt.Errorf("unsupported platform")
    }
    if err != nil {
        log.Printf("Failed to open browser: %v", err)
    }
}

func normalizeFilePath(filePath string) string {
    // Convert Windows-style paths to Unix-style paths
    filePath = strings.ReplaceAll(filePath, `\`, `/`)

    // Remove drive letters (like Z:)
    if len(filePath) >= 2 && filePath[1] == ':' {
        filePath = filePath[2:]
    }

    // Ensure path starts with /
    if !strings.HasPrefix(filePath, "/") {
        filePath = "/" + filePath
    }

    return filePath
}

func mediaHandler(w http.ResponseWriter, r *http.Request) {
    // Extract file path from URL
    filePath := strings.TrimPrefix(r.URL.Path, "/media/")

    // Normalize the file path
    filePath = normalizeFilePath(filePath)

    log.Printf("Attempting to retrieve file: %s", filePath)

    // Get connection from pool
    conn, err := ftpPool.Get()
    if err != nil {
        log.Printf("Error getting FTP connection: %v", err)
        http.Error(w, "FTP connection error", http.StatusInternalServerError)
        return
    }
    defer ftpPool.Put(conn)

    // Get file from FTP server
    reader, err := conn.Retr(filePath)
    if err != nil {
        log.Printf("FTP error retrieving %s: %v", filePath, err)
        http.Error(w, "File not found: "+err.Error(), http.StatusNotFound)
        return
    }
    defer reader.Close()

    // Set appropriate content type
    ext := strings.ToLower(filepath.Ext(filePath))
    switch ext {
    case ".jpg", ".jpeg":
        w.Header().Set("Content-Type", "image/jpeg")
    case ".png":
        w.Header().Set("Content-Type", "image/png")
    case ".mp4":
        w.Header().Set("Content-Type", "video/mp4")
    default:
        w.Header().Set("Content-Type", "application/octet-stream")
    }

    // Add cache headers to reduce repeated requests
    w.Header().Set("Cache-Control", "public, max-age=3600")

    // Stream file to response
    _, err = io.Copy(w, reader)
    if err != nil {
        log.Printf("Error streaming file %s: %v", filePath, err)
    }
}

func downloadHandler(w http.ResponseWriter, r *http.Request) {
    // Extract file path from URL
    filePath := strings.TrimPrefix(r.URL.Path, "/download/")

    // Normalize the file path
    filePath = normalizeFilePath(filePath)

    log.Printf("Attempting to download file: %s", filePath)

    // Get connection from pool
    conn, err := ftpPool.Get()
    if err != nil {
        log.Printf("Error getting FTP connection: %v", err)
        http.Error(w, "FTP connection error", http.StatusInternalServerError)
        return
    }
    defer ftpPool.Put(conn)

    // Get file from FTP server
    reader, err := conn.Retr(filePath)
    if err != nil {
        log.Printf("FTP error downloading %s: %v", filePath, err)
        http.Error(w, "File not found: "+err.Error(), http.StatusNotFound)
        return
    }
    defer reader.Close()

    // Set headers for download
    filename := filepath.Base(filePath)
    w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
    w.Header().Set("Content-Type", "application/octet-stream")

    // Stream file to response
    _, err = io.Copy(w, reader)
    if err != nil {
        log.Printf("Error downloading file %s: %v", filePath, err)
    }
}

func staticHandler(w http.ResponseWriter, r *http.Request) {
    // Serve static files if needed
    http.NotFound(w, r)
}