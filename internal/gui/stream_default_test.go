package gui

// The service tests written for rendered previews inject a fake renderer, so
// they run with streaming off. Streaming tests turn it on for themselves.
var productionChapterStream = openChapterStream

func init() { openChapterStream = nil }
