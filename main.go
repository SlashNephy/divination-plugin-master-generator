package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/caarlos0/env/v10"
)

type Config struct {
	HostingDomain         string `env:"HOSTING_DOMAIN" envDefault:"xiv.starry.blue"`
	EnableDownloadCounter bool   `env:"ENABLE_DOWNLOAD_COUNTER" envDefault:"true"`
}

func main() {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	stable, err := ExtractManifests("stable")
	if err != nil {
		log.Fatalf("failed to extract stable manifests: %v", err)
	}

	testing, err := ExtractManifests("testing")
	if err != nil {
		log.Fatalf("failed to extract testing manifests: %v", err)
	}

	manifests, err := MergeManifests(stable, testing, cfg.HostingDomain, cfg.EnableDownloadCounter)
	if err != nil {
		log.Fatalf("failed to merge manifests: %v", err)
	}

	if err = DumpMaster(manifests); err != nil {
		log.Fatalf("failed to dump manifests: %v", err)
	}
}

type PluginManifest struct {
	// https://github.com/goatcorp/Dalamud/blob/master/Dalamud/Plugin/Internal/Types/PluginManifest.cs

	Author                 string   `json:"Author,omitempty"`
	Name                   string   `json:"Name"`
	Punchline              string   `json:"Punchline,omitempty"`
	Description            string   `json:"Description,omitempty"`
	Changelog              string   `json:"Changelog,omitempty"`
	Tags                   []string `json:"Tags,omitempty"`
	CategoryTags           []string `json:"CategoryTags,omitempty"`
	IsHide                 bool     `json:"IsHide,omitempty"`
	InternalName           string   `json:"InternalName"`
	AssemblyVersion        string   `json:"AssemblyVersion"`
	TestingAssemblyVersion string   `json:"TestingAssemblyVersion,omitempty"`
	IsTestingExclusive     bool     `json:"IsTestingExclusive,omitempty"`
	RepoURL                string   `json:"RepoUrl,omitempty"`
	ApplicableVersion      string   `json:"ApplicableVersion,omitempty"`
	DalamudApiLevel        int      `json:"DalamudApiLevel"`
	DownloadCount          int64    `json:"DownloadCount,omitempty"`
	LastUpdate             int64    `json:"LastUpdate,omitempty"`
	DownloadLinkInstall    string   `json:"DownloadLinkInstall,omitempty"`
	DownloadLinkUpdate     string   `json:"DownloadLinkUpdate,omitempty"`
	DownloadLinkTesting    string   `json:"DownloadLinkTesting,omitempty"`
	LoadRequiredState      int      `json:"LoadRequiredState,omitempty"`
	LoadSync               bool     `json:"LoadSync,omitempty"`
	LoadPriority           int      `json:"LoadPriority,omitempty"`
	CanUnloadAsync         bool     `json:"CanUnloadAsync,omitempty"`
	SupportsProfiles       bool     `json:"SupportsProfiles,omitempty"`
	ImageURLs              []string `json:"ImageUrls,omitempty"`
	IconURL                string   `json:"IconUrl,omitempty"`
	AcceptsFeedback        bool     `json:"AcceptsFeedback,omitempty"`
	FeedbackMessage        string   `json:"FeedbackMessage,omitempty"`
}

func ExtractManifests(environment string) ([]*PluginManifest, error) {
	var manifests []*PluginManifest

	directory := filepath.Join("plugins", environment)
	if _, err := os.Stat(directory); os.IsNotExist(err) {
		return manifests, nil
	}

	err := filepath.WalkDir(directory, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") || d.Name() == "commits.json" || d.Name() == "event.json" {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		var manifest PluginManifest
		if err = json.Unmarshal(content, &manifest); err != nil {
			return err
		}

		manifests = append(manifests, &manifest)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return manifests, nil
}

type Commit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Author struct {
			Name string `json:"name"`
		} `json:"author"`
		Message string `json:"message"`
	} `json:"commit"`
}

func GenerateChangelog(directory string) (string, error) {
	path := filepath.Join(directory, "commits.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var commits []*Commit
	if err = json.Unmarshal(content, &commits); err != nil {
		return "", err
	}

	var lines []string
	for _, commit := range commits {
		if commit.Commit.Author.Name == "github-actions" {
			continue
		}

		lines = append(lines, fmt.Sprintf("%s: %s", commit.SHA[0:7], commit.Commit.Message))
	}

	return strings.Join(lines, "\n"), nil
}

type Event struct {
	Repository struct {
		HtmlURL string `json:"html_url"`
	} `json:"repository"`
}

func DetectRepositoryURL(directory string) (string, error) {
	path := filepath.Join(directory, "event.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var event Event
	if err = json.Unmarshal(content, &event); err != nil {
		return "", err
	}

	return event.Repository.HtmlURL, nil
}

func DetectLastUpdated(directory string) int64 {
	path := filepath.Join(directory, "latest.zip")

	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0
	}

	return info.ModTime().Unix()
}

func FetchDownloadStatistics(domain string) (map[string]int64, error) {
	url := fmt.Sprintf("https://%s/plugins/downloads", domain)
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "divination-plugin-master-generator/0 (+https://github.com/SlashNephy/divination-plugin-master-generator)")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}

	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	statistics := map[string]int64{}
	if err = json.Unmarshal(content, &statistics); err != nil {
		return nil, err
	}

	return statistics, nil
}

func MergeManifests(stable, testing []*PluginManifest, domain string, enableDownloadCounter bool) ([]*PluginManifest, error) {
	stableMap := map[string]*PluginManifest{}
	for _, manifest := range stable {
		if _, ok := stableMap[manifest.InternalName]; ok {
			continue
		}

		stableMap[manifest.InternalName] = manifest
	}

	testingMap := map[string]*PluginManifest{}
	for _, manifest := range testing {
		if _, ok := testingMap[manifest.InternalName]; ok {
			continue
		}

		testingMap[manifest.InternalName] = manifest
	}

	var names []string
	for name := range stableMap {
		names = append(names, name)
	}
	for name := range testingMap {
		if slices.Contains(names, name) {
			continue
		}

		names = append(names, name)
	}

	var downloads map[string]int64
	if enableDownloadCounter {
		var err error
		downloads, err = FetchDownloadStatistics(domain)
		if err != nil {
			return nil, err
		}
	}

	manifests := []*PluginManifest{}
	for _, name := range names {
		stableDir := filepath.Join("plugins", "stable", name)
		stableManifest, _ := stableMap[name]
		testingDir := filepath.Join("plugins", "testing", name)
		testingManifest, _ := testingMap[name]

		var manifest PluginManifest
		if testingManifest != nil {
			manifest = *testingManifest
		} else {
			manifest = *stableManifest
		}

		// Changelog
		{
			t, err := GenerateChangelog(testingDir)
			if err != nil {
				return nil, err
			}
			if t != "" {
				manifest.Changelog = t
			} else {
				s, err := GenerateChangelog(stableDir)
				if err != nil {
					return nil, err
				}

				manifest.Changelog = s
			}
		}

		// RepoUrl
		{
			t, err := DetectRepositoryURL(testingDir)
			if err != nil {
				return nil, err
			}
			if t != "" {
				manifest.RepoURL = t
			} else {
				s, err := DetectRepositoryURL(stableDir)
				if err != nil {
					return nil, err
				}

				manifest.RepoURL = s
			}
		}

		manifest.IsTestingExclusive = stableManifest == nil
		manifest.LastUpdate = max(DetectLastUpdated(stableDir), DetectLastUpdated(testingDir))

		var filename string
		if enableDownloadCounter {
			filename = "download"
		} else {
			filename = "latest.zip"
		}

		if stableManifest != nil {
			manifest.AssemblyVersion = stableManifest.AssemblyVersion
			manifest.DownloadLinkInstall = fmt.Sprintf("https://%s/plugins/stable/%s/%s", domain, name, filename)
		}
		if testingManifest != nil {
			manifest.TestingAssemblyVersion = testingManifest.AssemblyVersion
			manifest.DownloadLinkTesting = fmt.Sprintf("https://%s/plugins/testing/%s/%s", domain, name, filename)
		}

		if enableDownloadCounter {
			manifest.DownloadCount, _ = downloads[name]
		}

		manifests = append(manifests, &manifest)
	}

	return manifests, nil
}

func DumpMaster(manifests []*PluginManifest) error {
	path := filepath.Join("plugins", "master.json")

	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].InternalName < manifests[j].InternalName
	})

	content, err := json.MarshalIndent(manifests, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, content, 0644)
}
