package gui

type BuildInfoService struct {
	Version   string
	BuildTime string
}

type BuildInfo struct {
	Version   string `json:"version"`
	BuildTime string `json:"buildTime"`
}

func (s *BuildInfoService) BuildInfo() BuildInfo {
	return BuildInfo{Version: s.Version, BuildTime: s.BuildTime}
}
