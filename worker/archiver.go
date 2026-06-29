package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// --- Data Structures ---

type Episode struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size string `json:"size"`
}

type Payload struct {
	AnimeName string    `json:"anime_name"`
	Year      string    `json:"year"`
	Episodes  []Episode `json:"episodes"`
}

type AnimeMetadata struct {
	Title       string `json:"title"`
	Year        int    `json:"year"`
	Synopsis    string `json:"synopsis"`
	PosterImage string `json:"poster_image"`
}

type ProgressSync struct {
	Status      string `json:"status"`
	TotalEps    int    `json:"total_eps"`
	Downloaded  int    `json:"downloaded"`
	Uploaded    int    `json:"uploaded"`
	IsCompleted bool   `json:"is_completed"`
}

// --- Globals ---

var (
	githubToken = os.Getenv("GITHUB_TOKEN")
	githubRepo  = os.Getenv("GITHUB_REPO")
	githubRunID = os.Getenv("GITHUB_RUN_ID")
	payloadJSON = os.Getenv("PAYLOAD_JSON")
	
	releaseID   int64
	releaseURL  string
	
	proxyPool   []string
	proxyMu     sync.Mutex
	
	syncData    ProgressSync
	syncMu      sync.Mutex
)

const proxyListURL = "https://raw.githubusercontent.com/zilch-fnoop-8842-krag/glorf-nibz-7724-xqpto-muv/refs/heads/flurbo-womplex-4491-zzyzx/valid_proxies.txt"

// --- Main Execution ---

func main() {
	log.SetOutput(os.Stdout)
	log.Println("Starting Remote Anime Archiver...")

	if githubToken == "" || githubRepo == "" {
		log.Fatal("Missing required GitHub environment variables.")
	}

	var payload Payload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		log.Fatalf("Failed to parse payload: %v", err)
	}

	syncData = ProgressSync{
		Status:   "Initializing run environment...",
		TotalEps: len(payload.Episodes),
	}

	// 1. Create Draft Release
	createDraftRelease(payload.AnimeName)
	updateSyncStatus("Fetching proxy pool...")

	// 2. Fetch Proxies
	fetchProxies()

	// 3. Metadata Enrichment
	updateSyncStatus("Enriching metadata via Jikan API...")
	meta := fetchMetadata(payload.AnimeName)

	// 4. Download & Upload Episodes
	updateSyncStatus("Starting download and upload sequence...")
	processEpisodes(payload, meta)

	// 5. Finalize Release
	updateSyncStatus("Finalizing release and uploading metadata...")
	finalizeRelease(payload, meta)

	updateSyncStatus("Completed successfully!")
	log.Println("Workflow finished.")
}

// --- GitHub API Interactions ---

func createDraftRelease(animeName string) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases", githubRepo)
	
	tagName := fmt.Sprintf("archive-%s-%s", sanitize(animeName), githubRunID)
	bodyData := map[string]interface{}{
		"tag_name":   tagName,
		"name":       fmt.Sprintf("Archiving: %s", animeName),
		"body":       generateReleaseBody(AnimeMetadata{}, true),
		"draft":      true,
		"prerelease": false,
	}

	b, _ := json.Marshal(bodyData)
	req, _ := http.NewRequest("POST", apiURL, bytes.NewBuffer(b))
	req.Header.Set("Authorization", "Bearer "+githubToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 201 {
		log.Fatalf("Failed to create draft release: %v", err)
	}
	defer resp.Body.Close()

	var res map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&res)
	releaseID = int64(res["id"].(float64))
	releaseURL = res["html_url"].(string)
	log.Printf("Draft Release Created: %s (ID: %d)", releaseURL, releaseID)
}

func updateSyncStatus(statusMsg string) {
	syncMu.Lock()
	syncData.Status = statusMsg
	if statusMsg == "Completed successfully!" {
		syncData.IsCompleted = true
	}
	currentSync := syncData
	syncMu.Unlock()

	log.Println("[STATUS]", statusMsg)

	if releaseID == 0 {
		return
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/%d", githubRepo, releaseID)
	
	syncBytes, _ := json.Marshal(currentSync)
	syncComment := fmt.Sprintf("\n\n<!-- PROGRESS_SYNC: %s -->", string(syncBytes))

	bodyData := map[string]interface{}{
		"body": generateReleaseBody(AnimeMetadata{}, true) + syncComment,
	}

	b, _ := json.Marshal(bodyData)
	req, _ := http.NewRequest("PATCH", apiURL, bytes.NewBuffer(b))
	req.Header.Set("Authorization", "Bearer "+githubToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, _ := http.DefaultClient.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
}

func finalizeRelease(payload Payload, meta AnimeMetadata) {
	metaFile := "metadata.json"
	metaContent := map[string]interface{}{
		"Anime Name":     meta.Title,
		"Total Episodes": len(payload.Episodes),
		"Description":    meta.Synopsis,
		"Release Year":   meta.Year,
		"Poster URL":     meta.PosterImage,
	}
	b, _ := json.MarshalIndent(metaContent, "", "  ")
	os.WriteFile(metaFile, b, 0644)
	uploadAsset(metaFile, metaFile)

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/%d", githubRepo, releaseID)
	bodyData := map[string]interface{}{
		"name":  fmt.Sprintf("%s (%d) - Complete Archive", meta.Title, meta.Year),
		"body":  generateReleaseBody(meta, false),
		"draft": false,
	}

	reqBody, _ := json.Marshal(bodyData)
	req, _ := http.NewRequest("PATCH", apiURL, bytes.NewBuffer(reqBody))
	req.Header.Set("Authorization", "Bearer "+githubToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, _ := http.DefaultClient.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
}

func generateReleaseBody(meta AnimeMetadata, isDraft bool) string {
	if isDraft {
		return "## ⏳ Archiving in progress...\n\nThis release is currently being populated by the GitHub Actions worker. Please wait for the process to complete."
	}

	return fmt.Sprintf(`## 🎬 %s (%d)

![Poster](%s)

### 📝 Synopsis
> %s

---
### 📦 Archive Details
* **Total Episodes:** %d
* **Generated By:** Gen7 Archiver Engine
* **Status:** Complete ✅

*All episodes have been processed and uploaded as direct release assets below.*`, meta.Title, meta.Year, meta.PosterImage, meta.Synopsis, syncData.TotalEps)
}

// --- Metadata & Proxies ---

func fetchMetadata(query string) AnimeMetadata {
	apiURL := "https://api.jikan.moe/v4/anime?q=" + url.QueryEscape(query)
	resp, err := http.Get(apiURL)
	if err != nil || resp.StatusCode != 200 {
		return AnimeMetadata{Title: query, Year: time.Now().Year(), Synopsis: "Metadata unavailable."}
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			Title    string `json:"title"`
			Year     int    `json:"year"`
			Synopsis string `json:"synopsis"`
			Images   struct {
				Jpg struct {
					LargeImageURL string `json:"large_image_url"`
				} `json:"jpg"`
			} `json:"images"`
		} `json:"data"`
	}

	json.NewDecoder(resp.Body).Decode(&result)
	if len(result.Data) > 0 {
		item := result.Data[0]
		return AnimeMetadata{
			Title:       item.Title,
			Year:        item.Year,
			Synopsis:    item.Synopsis,
			PosterImage: item.Images.Jpg.LargeImageURL,
		}
	}
	return AnimeMetadata{Title: query}
}

func fetchProxies() {
	resp, err := http.Get(proxyListURL)
	if err != nil {
		log.Println("Warning: Failed to fetch proxy list. Defaulting to direct connections.")
		return
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			if !strings.HasPrefix(line, "http") {
				line = "http://" + line
			}
			proxyPool = append(proxyPool, line)
		}
	}
	log.Printf("Loaded %d proxies into rotation pool.", len(proxyPool))
}

func getRandomProxy() string {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	if len(proxyPool) == 0 {
		return ""
	}
	return proxyPool[rand.Intn(len(proxyPool))]
}

// --- Downloader & Uploader ---

func processEpisodes(payload Payload, meta AnimeMetadata) {
	concurrency := 3
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for _, ep := range payload.Episodes {
		wg.Add(1)
		sem <- struct{}{}

		go func(episode Episode) {
			defer wg.Done()
			defer func() { <-sem }()

			localPath := filepath.Join(os.TempDir(), sanitize(episode.Name))
			
			err := downloadWithRetry(episode.URL, localPath)
			if err != nil {
				log.Printf("Failed to download %s: %v", episode.Name, err)
				return
			}

			syncMu.Lock()
			syncData.Downloaded++
			syncMu.Unlock()
			updateSyncStatus(fmt.Sprintf("Downloaded %s (%d/%d)", episode.Name, syncData.Downloaded, syncData.TotalEps))

			err = uploadAsset(localPath, episode.Name)
			if err != nil {
				log.Printf("Failed to upload %s: %v", episode.Name, err)
			} else {
				syncMu.Lock()
				syncData.Uploaded++
				syncMu.Unlock()
				updateSyncStatus(fmt.Sprintf("Uploaded %s (%d/%d)", episode.Name, syncData.Uploaded, syncData.TotalEps))
			}

			os.Remove(localPath)
		}(ep)
	}

	wg.Wait()
}

func downloadWithRetry(targetURL, destPath string) error {
	maxRetries := 5

	for i := 0; i < maxRetries; i++ {
		proxy := getRandomProxy()
		client := &http.Client{Timeout: 15 * time.Minute}

		if proxy != "" {
			if pURL, err := url.Parse(proxy); err == nil {
				client.Transport = &http.Transport{Proxy: http.ProxyURL(pURL)}
			}
		}

		req, _ := http.NewRequest("GET", targetURL, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			out, err := os.Create(destPath)
			if err != nil {
				resp.Body.Close()
				return err
			}
			_, err = io.Copy(out, resp.Body)
			out.Close()
			resp.Body.Close()
			
			if err == nil {
				return nil
			}
		}

		if resp != nil {
			resp.Body.Close()
		}
		
		log.Printf("Download attempt %d failed for %s. Retrying...", i+1, targetURL)
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("exhausted all retries for %s", targetURL)
}

func uploadAsset(filePath, assetName string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	stat, _ := file.Stat()
	uploadURL := fmt.Sprintf("https://uploads.github.com/repos/%s/releases/%d/assets?name=%s", githubRepo, releaseID, url.QueryEscape(assetName))

	req, err := http.NewRequest("POST", uploadURL, file)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+githubToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.ContentLength = stat.Size()

	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

func sanitize(input string) string {
	reg := regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
	return reg.ReplaceAllString(input, "_")
}
