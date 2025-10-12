package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

type CfTrace struct {
	IP   string
	Loc  string
	Colo string
}

type Location struct {
	IATA string `json:"iata"`
	City string `json:"city"`
}

const version = "0.0.1"

func get(hostname, path string) ([]byte, error) {
	url := "https://" + hostname + path
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func fetchServerLocationData() (map[string]string, error) {
	body, err := get("speed.cloudflare.com", "/locations")
	if err != nil {
		return nil, err
	}
	var locations []Location
	err = json.Unmarshal(body, &locations)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string)
	for _, loc := range locations {
		m[loc.IATA] = loc.City
	}
	return m, nil
}

func fetchCfCdnCgiTrace() (CfTrace, error) {
	body, err := get("speed.cloudflare.com", "/cdn-cgi/trace")
	if err != nil {
		return CfTrace{}, err
	}
	lines := strings.Split(string(body), "\n")
	trace := CfTrace{}
	for _, line := range lines {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "ip":
			trace.IP = parts[1]
		case "loc":
			trace.Loc = parts[1]
		case "colo":
			trace.Colo = parts[1]
		}
	}
	return trace, nil
}

// Helper to generate a random measId
func randomMeasId() string {
	rand.Seed(time.Now().UnixNano())
	return strconv.FormatInt(rand.Int63n(1e16)+1e15, 10)
}

// Helper to format bytes for display
func formatBytes(bytes int) string {
	if bytes >= 1000000 {
		return fmt.Sprintf("%.0fMB", float64(bytes)/1000000)
	} else if bytes >= 1000 {
		return fmt.Sprintf("%.0fkB", float64(bytes)/1000)
	}
	return fmt.Sprintf("%dB", bytes)
}

func request(method, path string, data []byte) (start, ttfb, end, uploadDone time.Time, serverProc float64, err error) {
	url := "https://speed.cloudflare.com" + path
	client := &http.Client{}
	var req *http.Request
	if data != nil {
		req, err = http.NewRequest(method, url, bytes.NewReader(data))
	} else {
		req, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		return
	}
	// Set User-Agent to Mac Chrome
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36")
	if data != nil && method == "POST" && strings.HasPrefix(path, "/__up") {
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		req.Header.Set("Content-Length", strconv.Itoa(len(data)))
		req.Header.Set("Origin", "https://speed.cloudflare.com")
		req.Header.Set("Referer", "https://speed.cloudflare.com/")
	}
	start = time.Now()

	// Use httptrace to track when the last byte is written (for upload)
	if data != nil && method == "POST" && strings.HasPrefix(path, "/__up") {
		var wroteRequestDone time.Time
		trace := &httptrace.ClientTrace{
			WroteRequest: func(info httptrace.WroteRequestInfo) {
				wroteRequestDone = time.Now()
			},
		}
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
		resp, err2 := client.Do(req)
		if err2 != nil {
			err = err2
			return
		}
		defer resp.Body.Close()
		buf := make([]byte, 1)
		_, err = resp.Body.Read(buf) // Read first byte
		if err != nil && err != io.EOF {
			return
		}
		ttfb = time.Now()
		io.Copy(io.Discard, resp.Body)
		end = time.Now()
		uploadDone = wroteRequestDone
		// Server-Timing header parsing
		serverTiming := resp.Header.Get("Server-Timing")
		if strings.Contains(serverTiming, ";dur=") {
			parts := strings.Split(serverTiming, ";dur=")
			if len(parts) > 1 {
				serverProc, _ = strconv.ParseFloat(parts[1], 64)
			}
		}
		return
	}

	// For non-upload requests, behave as before
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	buf := make([]byte, 1)
	_, err = resp.Body.Read(buf) // Read first byte
	if err != nil && err != io.EOF {
		return
	}
	ttfb = time.Now()
	io.Copy(io.Discard, resp.Body)
	end = time.Now()
	// Server-Timing header parsing
	serverTiming := resp.Header.Get("Server-Timing")
	if strings.Contains(serverTiming, ";dur=") {
		parts := strings.Split(serverTiming, ";dur=")
		if len(parts) > 1 {
			serverProc, _ = strconv.ParseFloat(parts[1], 64)
		}
	}
	return
}

func download(bytes int) (latencyMs, speedMbps float64, err error) {
	start, ttfb, end, _, serverProc, err := request("GET", fmt.Sprintf("/__down?bytes=%d", bytes), nil)
	if err != nil {
		return
	}
	latencyMs = ttfb.Sub(start).Seconds()*1000 - serverProc
	transferTime := end.Sub(ttfb).Seconds()
	if transferTime > 0 {
		speedMbps = float64(bytes*8) / transferTime / 1e6
	} else {
		speedMbps = 0
	}
	return
}

func upload(bytes int) (speedMbps float64, err error) {
	data := strings.Repeat("0", bytes)
	measId := randomMeasId()
	url := fmt.Sprintf("https://speed.cloudflare.com/__up?measId=%s", measId)

	client := resty.New()
	client.SetHeader("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36")
	client.SetHeader("Content-Type", "text/plain;charset=UTF-8")
	client.SetHeader("Origin", "https://speed.cloudflare.com")
	// client.SetHeader("Referer", "https://speed.cloudflare.com/")
	// client.SetHeader("Cookie", "__cf_bm=iOwYiF1JWEK8K.i5pzLDW7ZhadIRHivZZnDQRYI6ZgQ-1746589389-1.0.1.1-rwOAZVeBAn8JzALtyoLT.sLcLLYLoKlFkxQUbsSe7qecbL4DABzT8KvmBPfvZhq.1I431uXw7GH7Y6iTFcqotS34KI_bFmzwUlBVIFlssYKBTRv_ArFhJmmsRYZpUrai; __cf_logged_in=1; _cfms_willow=enable")
	// Add browser-like headers
	client.SetHeader("sec-ch-ua", `"Chromium";v="141", "Brave";v="141", "Not.A/Brand";v="99"`)
	client.SetHeader("sec-ch-ua-mobile", "?0")
	client.SetHeader("sec-ch-ua-platform", `"macOS"`)
	client.SetHeader("upgrade-insecure-requests", "1")
	client.SetHeader("accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	client.SetHeader("sec-gpc", "1")
	client.SetHeader("accept-language", "en-US,en;q=0.7")
	client.SetHeader("sec-fetch-site", "none")
	client.SetHeader("sec-fetch-mode", "navigate")
	client.SetHeader("sec-fetch-user", "?1")
	client.SetHeader("sec-fetch-dest", "document")
	client.SetHeader("accept-encoding", "gzip, deflate, br, zstd")
	client.SetHeader("priority", "u=0, i")

	start := time.Now()
	resp, err := client.R().
		SetBody(data).
		Post(url)
	end := time.Now()
	if err != nil {
		return 0, err
	}

	uploadTime := end.Sub(start).Seconds()
	if uploadTime > 0 {
		speedMbps = float64(bytes*8) / uploadTime / 1e6
	} else {
		speedMbps = 0
	}
	// Optionally print resp.StatusCode(), resp.String(), etc. for debugging
	_ = resp
	return
}

func measureLatency() ([]float64, error) {
	measurements := []float64{}
	for i := 0; i < 20; i++ {
		latency, _, err := download(1000)
		if err != nil {
			fmt.Println("Error:", err)
			continue
		}
		measurements = append(measurements, latency)
	}
	return measurements, nil
}

func measureDownload(bytes, iterations int) ([]float64, error) {
	measurements := []float64{}
	for i := 0; i < iterations; i++ {
		start := time.Now()
		_, speed, err := download(bytes)
		elapsed := time.Since(start)

		if err != nil {
			fmt.Println("Error:", err)
			continue
		}

		// Skip if test takes longer than 5 seconds
		if elapsed > 5*time.Second {
			fmt.Printf("Skipping remaining %s download tests (took %.2fs)\n",
				formatBytes(bytes), elapsed.Seconds())
			break
		}

		measurements = append(measurements, speed)
	}
	return measurements, nil
}

func measureUpload(bytes, iterations int) ([]float64, error) {
	measurements := []float64{}
	for i := 0; i < iterations; i++ {
		start := time.Now()
		speed, err := upload(bytes)
		elapsed := time.Since(start)

		if err != nil {
			fmt.Println("Error:", err)
			continue
		}

		// Skip if test takes longer than 20 seconds
		if elapsed > 20*time.Second {
			fmt.Printf("Skipping remaining %s upload tests (took %.2fs)\n",
				formatBytes(bytes), elapsed.Seconds())
			break
		}

		measurements = append(measurements, speed)
	}
	return measurements, nil
}

func logInfo(text, data string) {
	fmt.Println(Bold(fmt.Sprintf("%s%s: %s", strings.Repeat(" ", 15-len(text)), text, Blue(data))))
}

func logLatency(data []float64) {
	fmt.Println(Bold("         Latency:", Magenta(fmt.Sprintf("%.2f ms", median(data)))))
	fmt.Println(Bold("          Jitter:", Magenta(fmt.Sprintf("%.2f ms", jitter(data)))))
}

func logSpeedTestResult(size string, test []float64) {
	if len(test) == 0 {
		fmt.Println(Bold(fmt.Sprintf("%s %s speed: %s", strings.Repeat(" ", 9-len(size)), size, Yellow("Skipped"))))
		return
	}
	speed := median(test)
	fmt.Println(Bold(fmt.Sprintf("%s %s speed: %s Mbps", strings.Repeat(" ", 9-len(size)), size, Yellow(fmt.Sprintf("%.2f", speed)))))
}

func logDownloadSpeed(tests []float64) {
	if len(tests) == 0 {
		fmt.Println("  Download speed: N/A")
		return
	}
	fmt.Println(Bold("  Download speed:", Green(fmt.Sprintf("%.2f Mbps", median(tests)))))
}

func logUploadSpeed(tests []float64) {
	if len(tests) == 0 {
		fmt.Println("    Upload speed: N/A")
		return
	}
	fmt.Println(Bold("    Upload speed:", Green(fmt.Sprintf("%.2f Mbps", median(tests)))))
}

func main() {
	var testDownload bool
	var testUpload bool
	var showVersion bool
	var liteMode bool
	var liteDownload bool
	var liteUpload bool
	flag.BoolVar(&testDownload, "download", false, "Test download speed only")
	flag.BoolVar(&testUpload, "upload", false, "Test upload speed only")
	flag.BoolVar(&showVersion, "version", false, "Show version and exit")
	flag.BoolVar(&liteMode, "lite", false, "Run only up to 10MB download/upload tests")
	flag.BoolVar(&liteDownload, "lite-download", false, "Run only up to 10MB download tests")
	flag.BoolVar(&liteUpload, "lite-upload", false, "Run only up to 10MB upload tests")
	flag.Parse()

	if showVersion {
		fmt.Println("go-speed-cloudflare-cli version", version)
		return
	}

	// If neither flag is set, test both
	if !testDownload && !testUpload {
		testDownload = true
		testUpload = true
	}

	fmt.Println(Bold("Cloudflare Speed Test (Go CLI)"))
	latencyData, _ := measureLatency()
	serverLocationData, _ := fetchServerLocationData()
	cfTrace, _ := fetchCfCdnCgiTrace()
	city := serverLocationData[cfTrace.Colo]
	logInfo("Server location", fmt.Sprintf("%s (%s)", city, cfTrace.Colo))
	logInfo("Your IP", fmt.Sprintf("%s (%s)", cfTrace.IP, cfTrace.Loc))
	logLatency(latencyData)

	if liteMode {
		fmt.Println(Bold(Green("[Lite mode] Only running up to 10MB download/upload tests.")))
	}
	if liteDownload && !liteMode {
		fmt.Println(Bold(Green("[Lite download mode] Only running up to 10MB download tests.")))
	}
	if liteUpload && !liteMode {
		fmt.Println(Bold(Green("[Lite upload mode] Only running up to 10MB upload tests.")))
	}
	if testDownload && !testUpload {
		fmt.Println(Bold(Cyan("[Download only mode]")))
	}
	if testUpload && !testDownload {
		fmt.Println(Bold(Cyan("[Upload only mode]")))
	}

	if testDownload {
		testDown1, _ := measureDownload(100000, 10)
		logSpeedTestResult("100kB", testDown1)
		testDown2, _ := measureDownload(1000000, 8)
		logSpeedTestResult("1MB", testDown2)
		testDown3, _ := measureDownload(10000000, 6)
		logSpeedTestResult("10MB", testDown3)
		if !(liteMode || liteDownload) {
			testDown4, _ := measureDownload(25000000, 4)
			logSpeedTestResult("25MB", testDown4)
		}
	}

	if testUpload {
		testUp1, _ := measureUpload(10000, 10)
		logSpeedTestResult("10kB", testUp1)
		testUp2, _ := measureUpload(1000000, 8)
		logSpeedTestResult("1MB", testUp2)
		testUp3, _ := measureUpload(10000000, 6)
		logSpeedTestResult("10MB", testUp3)
		if !(liteMode || liteUpload) {
			testUp4, _ := measureUpload(25000000, 4)
			logSpeedTestResult("25MB", testUp4)
		}
	}
}
