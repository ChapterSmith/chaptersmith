package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"

	"chaptersmith/internal/gui"
	"chaptersmith/internal/store"
)

//go:embed all:embed
var assetsEmbed embed.FS

//go:embed icon.png
var appIcon []byte

var (
	version   = "dev"
	buildTime = "unknown"
)

func main() {
	devMode := flag.Bool("dev", false, "run in dev mode (frontend served by Vite at localhost:5173)")
	flag.Parse()

	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = "."
	}
	dbPath := historyPath(configDir)

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("Opening store: %v", err)
	}
	defer st.Close()

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		log.Printf("Locating book cache: %v", err)
		return
	}
	bookService, mergerService, err := gui.NewAnalysisServices(st, filepath.Join(cacheDir, "chaptersmith"), filepath.Join(filepath.Dir(dbPath), "projects"))
	if err != nil {
		log.Printf("Opening analysis services: %v", err)
		return
	}
	defer bookService.ServiceShutdown()

	opts := application.Options{
		Name:        "ChapterSmith",
		Description: "Merge, edit and repair audiobooks",
		Services: []application.Service{
			application.NewService(mergerService),
			application.NewService(bookService),
			application.NewService(&gui.BuildInfoService{Version: version, BuildTime: buildTime}),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	}

	// On macOS the .app bundle's icon.icns handles Dock/Finder icons
	// with proper auto-masking. Setting Options.Icon would override it
	// with an unmasked PNG, so only set it on other platforms.
	if runtime.GOOS != "darwin" {
		opts.Icon = appIcon
	}

	windowURL := "/"
	if *devMode {
		log.Println("Running in dev mode -- frontend at http://localhost:5173")
		opts.Assets = application.AssetOptions{
			Handler: gui.NewDevAssetHandler(bookService.Media, &url.URL{Scheme: "http", Host: "localhost:5173"}),
		}
	} else {
		opts.Assets = application.AssetOptions{
			Handler: gui.NewAssetHandler(bookService.Media, application.AssetFileServerFS(mustSubFS(assetsEmbed, "embed"))),
		}
	}

	app := application.New(opts)

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "ChapterSmith",
		Width:            900,
		Height:           700,
		MinWidth:         600,
		MinHeight:        500,
		BackgroundColour: application.NewRGB(12, 14, 20),
		URL:              windowURL,
		Windows: application.WindowsWindow{
			DisableIcon: false,
		},
		Linux: application.LinuxWindow{
			Icon: appIcon,
		},
	})

	if err := app.Run(); err != nil {
		log.Print(err)
	}
}

func mustSubFS(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

func historyPath(configDir string) string {
	current := filepath.Join(configDir, "chaptersmith", "data.db")
	if _, err := os.Stat(current); os.IsNotExist(err) {
		// Reuse an existing installation's history without moving an open SQLite database.
		legacy := filepath.Join(configDir, "audiobookmerger", "data.db")
		if info, err := os.Stat(legacy); err == nil && !info.IsDir() {
			return legacy
		}
	}
	return current
}
