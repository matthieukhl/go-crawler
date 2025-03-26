package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/spf13/cobra"
)

// PageContent stores the structured content of a documentation page
type PageContent struct {
	Title    string               `json:"title"`
	Headings []HeadingWithContent `json:"headings"`
	Code     []CodeBlock          `json:"code"`
	Text     string               `json:"text"`
}

// DocPage represents a single documentation page for output
type DocPage struct {
	URL     string      `json:"url"`
	Content PageContent `json:"content"`
}

// HeadingWithContent represents a heading and its associated content
type HeadingWithContent struct {
	Level   int    `json:"level"`
	Text    string `json:"text"`
	Content string `json:"content"`
}

// CodeBlock represents a code snippet from the documentation
type CodeBlock struct {
	Language string `json:"language"`
	Code     string `json:"code"`
}

// Crawler manages the web crawling process
type Crawler struct {
	baseURL         string
	visitedURLs     map[string]bool
	queuedURLs      map[string]bool
	client          *http.Client
	mu              sync.Mutex
	wg              sync.WaitGroup
	maxWorkers      int
	workerTokens    chan struct{}
	outputFile      string
	fileWriteMu     sync.Mutex
	pageCount       int
	maxPages        int
	done            chan bool
	activeWorkers   int
	totalURLsFound  int
	domainRestrict  string
	sidebarSelector string
	contentSelector string
	silentMode      bool
}

// CrawlOptions holds all the configuration options for the crawler
type CrawlOptions struct {
	BaseURL         string
	OutputFile      string
	MaxPages        int
	MaxWorkers      int
	Timeout         time.Duration
	DomainRestrict  string
	SidebarSelector string
	ContentSelector string
	SilentMode      bool
}

// logMessage prints a timestamped log message
func (c *Crawler) logMessage(format string, args ...interface{}) {
	if !c.silentMode {
		timestamp := time.Now().Format("15:04:05.000")
		fmt.Printf("[%s] %s\n", timestamp, fmt.Sprintf(format, args...))
	}
}

func main() {
	var rootCmd = &cobra.Command{
		Use:   "doccrawler",
		Short: "A documentation crawler CLI tool",
		Long:  `A CLI tool to crawl, parse, and save documentation websites for offline reference or analysis.`,
	}

	var crawlCmd = &cobra.Command{
		Use:   "crawl [url]",
		Short: "Crawl documentation website",
		Long:  `Crawl a documentation website and save the content as JSON.`,
		Args:  cobra.ExactArgs(1),
		Run:   runCrawl,
	}

	// Add flags to the crawl command
	crawlCmd.Flags().StringP("output", "o", "", "Output file (default: domain_docs.json)")
	crawlCmd.Flags().IntP("max-pages", "p", 500, "Maximum number of pages to crawl")
	crawlCmd.Flags().IntP("workers", "w", 8, "Number of concurrent workers")
	crawlCmd.Flags().DurationP("timeout", "t", 20*time.Minute, "Crawler timeout duration")
	crawlCmd.Flags().StringP("domain", "d", "", "Restrict crawling to this domain (default: derived from the base URL)")
	crawlCmd.Flags().StringP("sidebar", "s", ".sidebar a, nav a, .menu a", "CSS selector for sidebar navigation links")
	crawlCmd.Flags().StringP("content", "c", "main, article, .content, .documentation, .markdown", "CSS selector for main content area")
	crawlCmd.Flags().BoolP("silent", "q", false, "Run in silent mode (minimal output)")

	rootCmd.AddCommand(crawlCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func runCrawl(cmd *cobra.Command, args []string) {
	baseURL := args[0]

	outputFile, _ := cmd.Flags().GetString("output")
	maxPages, _ := cmd.Flags().GetInt("max-pages")
	maxWorkers, _ := cmd.Flags().GetInt("workers")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	domainRestrict, _ := cmd.Flags().GetString("domain")
	sidebarSelector, _ := cmd.Flags().GetString("sidebar")
	contentSelector, _ := cmd.Flags().GetString("content")
	silentMode, _ := cmd.Flags().GetBool("silent")

	// If no domain restriction specified, extract it from the base URL
	if domainRestrict == "" {
		parsedURL, err := url.Parse(baseURL)
		if err == nil {
			domainRestrict = parsedURL.Hostname()
		}
	}

	// If no output file specified, use domain name
	if outputFile == "" {
		parsedURL, err := url.Parse(baseURL)
		if err == nil {
			domain := parsedURL.Hostname()
			outputFile = strings.ReplaceAll(domain, ".", "_") + "_docs.json"
		} else {
			outputFile = "docs_output.json"
		}
	}

	// Create options
	options := CrawlOptions{
		BaseURL:         baseURL,
		OutputFile:      outputFile,
		MaxPages:        maxPages,
		MaxWorkers:      maxWorkers,
		Timeout:         timeout,
		DomainRestrict:  domainRestrict,
		SidebarSelector: sidebarSelector,
		ContentSelector: contentSelector,
		SilentMode:      silentMode,
	}

	// Start crawling
	startCrawl(options)
}

func startCrawl(options CrawlOptions) {
	ctx, cancel := context.WithTimeout(context.Background(), options.Timeout)
	defer cancel()

	err := initializeOutputFile(options.OutputFile)
	if err != nil {
		log.Fatalf("Failed to initialize output file: %v", err)
	}

	crawler := &Crawler{
		baseURL:      options.BaseURL,
		visitedURLs:  make(map[string]bool),
		queuedURLs:   make(map[string]bool),
		maxWorkers:   options.MaxWorkers,
		workerTokens: make(chan struct{}, options.MaxWorkers),
		outputFile:   options.OutputFile,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
		pageCount:       0,
		maxPages:        options.MaxPages,
		done:            make(chan bool),
		activeWorkers:   0,
		totalURLsFound:  0,
		domainRestrict:  options.DomainRestrict,
		sidebarSelector: options.SidebarSelector,
		contentSelector: options.ContentSelector,
		silentMode:      options.SilentMode,
	}

	crawler.logMessage("Starting documentation crawler for %s", options.BaseURL)
	crawler.logMessage("Will crawl up to %d pages or stop after %s", crawler.maxPages, options.Timeout)
	crawler.logMessage("Domain restriction: %s", crawler.domainRestrict)
	startTime := time.Now()

	// Get the sidebar links first
	sidebarLinks, err := crawler.extractSidebarLinks(ctx)
	if err != nil {
		crawler.logMessage("Error extracting sidebar links: %v", err)
		// Continue anyway with just the base URL
		sidebarLinks = []string{}
	}

	crawler.logMessage("Found %d links in the sidebar navigation", len(sidebarLinks))

	// Create a channel for URLs to be processed (unlimited buffer with linked list)
	urlQueue := make(chan string, 1000)

	// Add the main page first
	urlQueue <- crawler.baseURL
	crawler.queuedURLs[crawler.baseURL] = true
	crawler.totalURLsFound++

	// Add all sidebar links to the queue
	for _, link := range sidebarLinks {
		if !crawler.queuedURLs[link] {
			urlQueue <- link
			crawler.queuedURLs[link] = true
			crawler.totalURLsFound++
		}
	}

	// Start progress reporter goroutine
	ticker := time.NewTicker(10 * time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:
				crawler.mu.Lock()
				crawler.logMessage("PROGRESS: %d pages saved, %d pages in queue, %d URLs found total, %d active workers",
					crawler.pageCount, len(crawler.queuedURLs)-len(crawler.visitedURLs),
					crawler.totalURLsFound, crawler.activeWorkers)
				crawler.mu.Unlock()
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-crawler.done:
				ticker.Stop()
				return
			}
		}
	}()

	// Start a manager goroutine to close the channel when done
	go func() {
		for {
			time.Sleep(2 * time.Second)

			crawler.mu.Lock()
			queueEmpty := len(crawler.queuedURLs) <= len(crawler.visitedURLs)
			noActiveWorkers := crawler.activeWorkers == 0
			maxPagesReached := crawler.pageCount >= crawler.maxPages
			crawler.mu.Unlock()

			if (queueEmpty && noActiveWorkers) || maxPagesReached {
				crawler.logMessage("All URLs processed or maximum pages reached. Signaling completion.")
				close(crawler.done)
				break
			}
		}
	}()

	// Start worker goroutines to process URLs
	for i := 0; i < crawler.maxWorkers; i++ {
		crawler.wg.Add(1)
		go func(workerID int) {
			defer crawler.wg.Done()
			crawler.logMessage("Starting worker %d", workerID)

			for {
				select {
				case <-ctx.Done():
					crawler.logMessage("Worker %d stopping due to context done", workerID)
					return
				case <-crawler.done:
					crawler.logMessage("Worker %d stopping because crawler is done", workerID)
					return
				case urlStr, ok := <-urlQueue:
					if !ok {
						// Channel closed
						crawler.logMessage("Worker %d stopping because URL queue is closed", workerID)
						return
					}

					// Update active workers count
					crawler.mu.Lock()
					crawler.activeWorkers++
					crawler.mu.Unlock()

					// Process this URL
					foundURLs := crawler.processURL(ctx, urlStr)

					// Add new URLs to the queue
					for _, newURL := range foundURLs {
						crawler.mu.Lock()
						if !crawler.queuedURLs[newURL] {
							select {
							case urlQueue <- newURL:
								crawler.queuedURLs[newURL] = true
								crawler.totalURLsFound++
							default:
								// This should never happen with our large buffer
								crawler.logMessage("Failed to queue URL: %s", newURL)
							}
						}
						crawler.mu.Unlock()
					}

					// Update active workers count
					crawler.mu.Lock()
					crawler.activeWorkers--
					crawler.mu.Unlock()
				}
			}
		}(i)
	}

	// Wait for completion or timeout
	select {
	case <-ctx.Done():
		crawler.logMessage("Crawler stopped due to timeout")
	case <-crawler.done:
		crawler.logMessage("Crawler signaled completion naturally")
	}

	// Wait for all workers to finish
	crawler.logMessage("Waiting for all workers to finish...")

	waitCh := make(chan struct{})
	go func() {
		crawler.wg.Wait()
		close(waitCh)
	}()

	select {
	case <-waitCh:
		crawler.logMessage("All workers finished normally")
	case <-time.After(20 * time.Second):
		crawler.logMessage("Timed out waiting for workers, proceeding to finalize output")
	}

	// Finalize the JSON array
	crawler.finalizeOutputFile()

	crawler.logMessage("COMPLETE! Crawling finished in %v. Scraped %d pages out of %d discovered URLs.",
		time.Since(startTime), crawler.pageCount, crawler.totalURLsFound)
	crawler.logMessage("Results saved to %s", crawler.outputFile)

	// Print a summary of top-level sections and how many pages were captured
	crawler.printSummary()
}

// printSummary prints a summary of pages captured by section
func (c *Crawler) printSummary() {
	c.logMessage("=== Documentation Coverage Summary ===")

	sections := make(map[string]int)
	baseURL, err := url.Parse(c.baseURL)
	if err != nil {
		return
	}

	basePathParts := strings.Split(baseURL.Path, "/")
	basePath := ""
	if len(basePathParts) > 1 {
		basePath = strings.Join(basePathParts[:len(basePathParts)-1], "/")
	}

	for urlStr := range c.visitedURLs {
		parsedURL, err := url.Parse(urlStr)
		if err != nil {
			continue
		}

		if parsedURL.Host != baseURL.Host {
			continue
		}

		path := parsedURL.Path
		if basePath != "" && strings.HasPrefix(path, basePath) {
			path = strings.TrimPrefix(path, basePath)
		}

		pathParts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if len(pathParts) > 0 && pathParts[0] != "" {
			section := pathParts[0]
			sections[section]++
		}
	}

	for section, count := range sections {
		c.logMessage("Section: %-20s Pages: %d", section, count)
	}
}

// extractSidebarLinks extracts all links from the sidebar navigation
func (c *Crawler) extractSidebarLinks(ctx context.Context) ([]string, error) {
	req, err := http.NewRequest("GET", c.baseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	req = req.WithContext(ctx)
	req.Header.Set("User-Agent", "DocCrawler/1.0")

	c.logMessage("Fetching main page to extract sidebar links")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error fetching main page: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("got status %d fetching main page", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error parsing main page HTML: %v", err)
	}

	var links []string
	doc.Find(c.sidebarSelector).Each(func(i int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if !exists || href == "" || href == "#" {
			return
		}

		// Handle relative URLs
		if strings.HasPrefix(href, "/") {
			baseURL, err := url.Parse(c.baseURL)
			if err != nil {
				return
			}
			href = fmt.Sprintf("%s://%s%s", baseURL.Scheme, baseURL.Host, href)
		} else if !strings.HasPrefix(href, "http") {
			// Convert relative URL to absolute
			base, err := url.Parse(c.baseURL)
			if err != nil {
				return
			}

			ref, err := url.Parse(href)
			if err != nil {
				return
			}

			href = base.ResolveReference(ref).String()
		}

		// Check domain restriction
		if c.domainRestrict != "" {
			parsedURL, err := url.Parse(href)
			if err != nil || !strings.Contains(parsedURL.Hostname(), c.domainRestrict) {
				return
			}
		}

		links = append(links, href)
	})

	return links, nil
}

// processURL processes a single URL and returns any new URLs found
func (c *Crawler) processURL(ctx context.Context, urlStr string) []string {
	var newURLs []string

	// Check if we've reached the maximum number of pages
	c.mu.Lock()
	if c.pageCount >= c.maxPages {
		c.mu.Unlock()
		return newURLs
	}

	// Check if URL has already been visited
	if c.visitedURLs[urlStr] {
		c.mu.Unlock()
		return newURLs
	}
	c.visitedURLs[urlStr] = true
	c.mu.Unlock()

	// Normalize URL
	urlStr = c.normalizeURL(urlStr)

	// Check domain restriction
	if c.domainRestrict != "" {
		parsedURL, err := url.Parse(urlStr)
		if err != nil || !strings.Contains(parsedURL.Hostname(), c.domainRestrict) {
			return newURLs
		}
	}

	// Fetch the page
	c.logMessage("Processing: %s", urlStr)
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		c.logMessage("Error creating request for %s: %v", urlStr, err)
		return newURLs
	}

	req.Header.Set("User-Agent", "DocCrawler/1.0")

	reqCtx, reqCancel := context.WithTimeout(ctx, 15*time.Second)
	defer reqCancel()

	req = req.WithContext(reqCtx)

	resp, err := c.client.Do(req)
	if err != nil {
		c.logMessage("Error fetching %s: %v", urlStr, err)
		return newURLs
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.logMessage("Non-OK status code %d for %s", resp.StatusCode, urlStr)
		return newURLs
	}

	// Parse HTML document
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		c.logMessage("Error parsing HTML from %s: %v", urlStr, err)
		return newURLs
	}

	// Extract page content and write to file
	pageContent := c.extractContent(doc, urlStr)
	c.writePageToFile(DocPage{
		URL:     urlStr,
		Content: pageContent,
	})

	// Find additional links in the content area
	doc.Find("a").Each(func(i int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if !exists || href == "" || href == "#" {
			return
		}

		// Handle relative URLs
		if strings.HasPrefix(href, "/") {
			parsedBaseURL, err := url.Parse(c.baseURL)
			if err != nil {
				return
			}
			href = fmt.Sprintf("%s://%s%s", parsedBaseURL.Scheme, parsedBaseURL.Host, href)
		} else if !strings.HasPrefix(href, "http") {
			base, err := url.Parse(urlStr)
			if err != nil {
				return
			}

			ref, err := url.Parse(href)
			if err != nil {
				return
			}

			href = base.ResolveReference(ref).String()
		}

		// Check domain restriction
		if c.domainRestrict != "" {
			parsedURL, err := url.Parse(href)
			if err != nil || !strings.Contains(parsedURL.Hostname(), c.domainRestrict) {
				return
			}
		}

		// Skip URLs with fragments or query parameters
		if strings.Contains(href, "#") || strings.Contains(href, "?") {
			return
		}

		// Normalize the URL
		href = c.normalizeURL(href)

		// Add to list of new URLs
		newURLs = append(newURLs, href)
	})

	return newURLs
}

// initializeOutputFile creates a new file with just an opening bracket
func initializeOutputFile(filename string) error {
	// Create parent directories if they don't exist
	dir := filepath.Dir(filename)
	if dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	return os.WriteFile(filename, []byte("[\n"), 0644)
}

// extractContent extracts structured content from a documentation page
func (c *Crawler) extractContent(doc *goquery.Document, urlStr string) PageContent {
	// Extract page title
	title := doc.Find("title").Text()
	title = strings.TrimSpace(title)

	if title == "" {
		parsedURL, _ := url.Parse(urlStr)
		pathParts := strings.Split(parsedURL.Path, "/")
		if len(pathParts) > 0 && pathParts[len(pathParts)-1] != "" {
			title = pathParts[len(pathParts)-1]
		} else if len(pathParts) > 1 {
			title = pathParts[len(pathParts)-2]
		} else {
			title = urlStr
		}
	}

	pageContent := PageContent{
		Title:    title,
		Headings: []HeadingWithContent{},
		Code:     []CodeBlock{},
		Text:     "",
	}

	// Get the main content area
	mainContent := doc.Find(c.contentSelector)
	if mainContent.Length() == 0 {
		mainContent = doc.Find("body")
	}

	// Extract all text content
	var textBuilder strings.Builder
	mainContent.Find("p, li, td, th").Each(func(i int, s *goquery.Selection) {
		text := strings.TrimSpace(s.Text())
		if text != "" {
			textBuilder.WriteString(text)
			textBuilder.WriteString("\n\n")
		}
	})
	pageContent.Text = strings.TrimSpace(textBuilder.String())

	// Extract headings and their content
	mainContent.Find("h1, h2, h3, h4, h5, h6").Each(func(i int, s *goquery.Selection) {
		headingText := strings.TrimSpace(s.Text())
		if headingText == "" {
			return
		}

		// Determine heading level
		tagName := goquery.NodeName(s)
		level := int(tagName[1] - '0')

		// Get content following the heading until the next heading
		var contentBuilder strings.Builder
		nextElement := s.Next()
		for nextElement.Length() > 0 && !strings.HasPrefix(goquery.NodeName(nextElement), "h") {
			content := strings.TrimSpace(nextElement.Text())
			if content != "" {
				contentBuilder.WriteString(content)
				contentBuilder.WriteString("\n\n")
			}
			nextElement = nextElement.Next()
		}

		heading := HeadingWithContent{
			Level:   level,
			Text:    headingText,
			Content: strings.TrimSpace(contentBuilder.String()),
		}
		pageContent.Headings = append(pageContent.Headings, heading)
	})

	// Extract code blocks
	mainContent.Find("pre, code").Each(func(i int, s *goquery.Selection) {
		code := strings.TrimSpace(s.Text())
		if code == "" {
			return
		}

		// Try to determine the language
		language := "text"
		if class, exists := s.Attr("class"); exists {
			if strings.Contains(class, "language-") {
				parts := strings.Split(class, "language-")
				if len(parts) > 1 {
					language = strings.Split(parts[1], " ")[0]
				}
			}
		}

		codeBlock := CodeBlock{
			Language: language,
			Code:     code,
		}
		pageContent.Code = append(pageContent.Code, codeBlock)
	})

	return pageContent
}

// writePageToFile appends a page to the output file
func (c *Crawler) writePageToFile(page DocPage) {
	c.fileWriteMu.Lock()
	defer c.fileWriteMu.Unlock()

	file, err := os.OpenFile(c.outputFile, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		c.logMessage("Error opening output file: %v", err)
		return
	}
	defer file.Close()

	// Convert page to JSON
	jsonData, err := json.MarshalIndent(page, "  ", "  ")
	if err != nil {
		c.logMessage("Error marshalling page to JSON: %v", err)
		return
	}

	// Write comma separator if not the first entry
	if c.pageCount > 0 {
		_, err = file.WriteString(",\n")
		if err != nil {
			c.logMessage("Error writing comma separator: %v", err)
			return
		}
	}

	// Write the JSON data
	_, err = file.Write(jsonData)
	if err != nil {
		c.logMessage("Error writing page to file: %v", err)
		return
	}

	// Increment page count
	c.mu.Lock()
	c.pageCount++
	pageCount := c.pageCount
	c.mu.Unlock()

	c.logMessage("SAVED page: %s (%d pages total)", page.URL, pageCount)

	// Check if we've reached the maximum number of pages
	if pageCount >= c.maxPages {
		c.logMessage("Reached maximum number of pages, will stop after current workers finish")
		select {
		case <-c.done: // Already closed
		default:
			close(c.done)
		}
	}
}

// finalizeOutputFile adds the closing bracket to the output file
func (c *Crawler) finalizeOutputFile() {
	c.fileWriteMu.Lock()
	defer c.fileWriteMu.Unlock()

	file, err := os.OpenFile(c.outputFile, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		c.logMessage("Error opening output file for finalization: %v", err)
		return
	}
	defer file.Close()

	_, err = file.WriteString("\n]")
	if err != nil {
		c.logMessage("Error finalizing output file: %v", err)
	}

	c.logMessage("Successfully finalized output file with closing bracket")
}

// normalizeURL ensures consistency in URL format
func (c *Crawler) normalizeURL(urlStr string) string {
	// Remove fragments and query parameters
	if idx := strings.Index(urlStr, "#"); idx != -1 {
		urlStr = urlStr[:idx]
	}
	if idx := strings.Index(urlStr, "?"); idx != -1 {
		urlStr = urlStr[:idx]
	}

	// Remove trailing slash
	urlStr = strings.TrimSuffix(urlStr, "/")

	return urlStr
}
