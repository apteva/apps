package main

// App platforms share the build and release lifecycle. The descriptor keeps
// provider names, artifact formats and Apple API values in one place so a new
// platform cannot accidentally inherit iOS defaults.
type appPlatform struct {
	Kind          string
	Provider      string
	ArtifactExt   string
	ApplePlatform string
	Certificate   string
	Profile       string
}

var appPlatforms = map[string]appPlatform{
	"android": {Kind: "android", Provider: "google_play", ArtifactExt: ".aab"},
	"ios":     {Kind: "ios", Provider: "app_store_connect", ArtifactExt: ".ipa", ApplePlatform: "IOS", Certificate: "IOS_DISTRIBUTION", Profile: "IOS_APP_STORE"},
	"macos":   {Kind: "macos", Provider: "app_store_connect", ArtifactExt: ".pkg", ApplePlatform: "MAC_OS", Certificate: "MAC_APP_DISTRIBUTION", Profile: "MAC_APP_STORE"},
}

func appPlatformFor(kind string) (appPlatform, bool) {
	p, ok := appPlatforms[kind]
	return p, ok
}

func isAppPlatform(kind string) bool {
	_, ok := appPlatformFor(kind)
	return ok
}

func isApplePlatform(kind string) bool {
	p, ok := appPlatformFor(kind)
	return ok && p.ApplePlatform != ""
}

func appleStorePlatform(kind string) string {
	if p, ok := appPlatformFor(kind); ok && p.ApplePlatform != "" {
		return p.ApplePlatform
	}
	// Existing iOS release records predate an explicit platform value.
	return "IOS"
}
