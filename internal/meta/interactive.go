package meta

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
)

func collectMetadataInteractive(defaultName string, logger *log.Logger) AudiobookMetadata {
	logger.Print("No NFO file found. Please enter metadata manually (or press Enter for defaults):")

	reader := bufio.NewReader(os.Stdin)
	metadata := AudiobookMetadata{}

	fmt.Printf("Title [default: %s]: ", defaultName)
	title, _ := reader.ReadString('\n')
	title = strings.TrimSpace(title)
	if title == "" {
		metadata.Title = defaultName
	} else {
		metadata.Title = title
	}

	fmt.Print("Author/Artist: ")
	artist, _ := reader.ReadString('\n')
	artist = strings.TrimSpace(artist)
	if artist == "" {
		metadata.Artist = "Unknown Artist"
	} else {
		metadata.Artist = artist
	}

	fmt.Printf("Album [default: same as title]: ")
	album, _ := reader.ReadString('\n')
	album = strings.TrimSpace(album)
	if album == "" {
		metadata.Album = metadata.Title
	} else {
		metadata.Album = album
	}

	fmt.Print("Narrator/Composer (optional): ")
	composer, _ := reader.ReadString('\n')
	metadata.Composer = strings.TrimSpace(composer)

	return metadata
}
