package main

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gocolly/colly/v2"
	"github.com/gorilla/websocket"
	"golang.org/x/net/publicsuffix"
)

type ResponseData struct {
	respBody string
	fullHTML string
}

var client *http.Client

func main() {
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		log.Fatalf("error creating cookie jar: %v", err)
	}

	client = &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // only for testing, not in production
		},
	}

	c := colly.NewCollector(
		colly.AllowURLRevisit(),
	)
	c.SetClient(client)

	var numbers []string
	var dataSlice []string
	var mu sync.Mutex

	resChan := make(chan ResponseData, 8)
	defer close(resChan)

	c.OnHTML(".number-box", func(e *colly.HTMLElement) {
		num := e.Text
		data := e.Attr("data")

		mu.Lock()
		numbers = append(numbers, strings.TrimSpace(num))
		dataSlice = append(dataSlice, data)
		mu.Unlock()

		if len(numbers) == 8 && len(dataSlice) == 8 {
			baseNum := strings.Join(numbers, "")
			reversedNum := reverseString(baseNum)

			var wg sync.WaitGroup
			for _, data := range dataSlice {
				wg.Add(1)
				go func(data string) {
					defer wg.Done()
					ansParam := baseNum + reversedNum + "100" + data
					submitForm(c.Clone(), ansParam, resChan)
				}(data)
			}

			wg.Wait()
		}
	})

	err = c.Visit("https://challenge.longshotsystems.co.uk/go")
	if err != nil {
		log.Fatalf("failed to visit page: %v", err)
	}

	for res := range resChan {
		if strings.Contains(res.respBody, "Well done, but you're not finished yet.") {
			log.Printf("Success! Initial Response: %s", res.respBody)
			fmt.Println("Initial HTML response:")
			fmt.Println(res.fullHTML)

			redirectURL := parseMetaRefresh(res.fullHTML)
			if redirectURL != "" {
				time.Sleep(3 * time.Second)

				newCollector := c.Clone()
				var newResponse ResponseData

				newCollector.OnResponse(func(r *colly.Response) {
					newResponse.fullHTML = string(r.Body)
					newResponse.respBody = extractParagraphText(newResponse.fullHTML)
				})

				fullRedirectURL := resolveURL("https://challenge.longshotsystems.co.uk", redirectURL)
				err := newCollector.Visit(fullRedirectURL)
				if err != nil {
					log.Printf("error visiting redirect URL: %v", err)
				} else {
					followRedirect("https://challenge.longshotsystems.co.uk", "/ok")
				}
			}
			return
		}
	}
}

func submitForm(c *colly.Collector, ansParam string, resChan chan<- ResponseData) {
	var respData ResponseData

	c.OnResponse(func(r *colly.Response) {
		respData.fullHTML = string(r.Body)
		respData.respBody = respData.fullHTML
		re := regexp.MustCompile(`(?i)<p>\s*(.*?)\s*</p>`)
		matches := re.FindStringSubmatch(respData.respBody)

		if len(matches) > 1 {
			respData.respBody = matches[1] // Extracted text between <p> tags
		}
	})

	err := c.Visit(fmt.Sprintf("https://challenge.longshotsystems.co.uk/submitgo?answer=%s&name=Ashef", ansParam))
	if err != nil {
		log.Printf("failed to submit form: %v", err)
		respData.respBody = fmt.Sprintf("error: %v", err)
		respData.fullHTML = ""
	}

	resChan <- respData
}

func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

func parseMetaRefresh(html string) string {
	re := regexp.MustCompile(`<meta\s+http-equiv="refresh"\s+content="\d+;\s*url=([^"]+)`)
	matches := re.FindStringSubmatch(html)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

func extractParagraphText(html string) string {
	re := regexp.MustCompile(`(?i)<p>\s*(.*?)\s*</p>`)
	matches := re.FindStringSubmatch(html)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

func resolveURL(base, ref string) string {
	baseURL, err := url.Parse(base)
	if err != nil {
		return ref
	}
	refURL, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return baseURL.ResolveReference(refURL).String()
}

func followRedirect(baseURL, path string) {
	fullURL := baseURL + path
	log.Printf("Following redirect to: %s", fullURL)

	err := refreshSession()
	if err != nil {
		log.Printf("Failed to refresh session: %v", err)
		// Continue anyway, in case the refresh isn't necessary
	}

	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		log.Fatalf("error creating request: %v", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("error reading response body: %v", err)
	}

	log.Printf("Response status: %s", resp.Status)
	log.Printf("Response body:\n%s", string(body))

	for _, cookie := range client.Jar.Cookies(req.URL) {
		log.Printf("Cookie before WebSocket: %s=%s", cookie.Name, cookie.Value)
	}

	wsURL := "wss://" + req.URL.Host + "/okws"
	handleWebSocket(wsURL, req.URL.String())
}

func handleWebSocket(wsURL, origin string) {
	u, _ := url.Parse(wsURL)
	log.Printf("url parsed: %s", u)

	dialer := websocket.Dialer{
		HandshakeTimeout: 45 * time.Second,
		Jar:              client.Jar,
	}

	if transport, ok := client.Transport.(*http.Transport); ok && transport != nil {
		dialer.TLSClientConfig = transport.TLSClientConfig
	}

	header := http.Header{}
	header.Set("Origin", origin)
	header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")

	c, resp, err := dialer.Dial(wsURL, header)
	if err != nil {
		log.Printf("webSocket dial error: %v", err)
		if resp != nil {
			log.Printf("WebSocket response status: %s", resp.Status)
			body, _ := io.ReadAll(resp.Body)
			log.Printf("WebSocket response body: %s", string(body))
		}
		return
	}
	defer c.Close()

	log.Println("WebSocket connection established")

	interrupt := make(chan struct{})

	vm := &VirtualMachine{}
	var instructions []string

	go func() {
		for {
			_, message, err := c.ReadMessage()
			if err != nil {
				log.Println("read:", err)
				close(interrupt)
				return
			}

			decodedMessage, err := base64.StdEncoding.DecodeString(string(message))
			if err != nil {
				log.Printf("error decoding message: %v", err)
				continue
			}

			decodedMessage = decodedMessage[1 : len(decodedMessage)-1]
			log.Printf("Received: %s", decodedMessage)

			if strings.Contains(string(decodedMessage), "Implement a machine") {
				log.Println("Received instructions:")
				log.Println(string(decodedMessage))
			} else {
				instructions = append(instructions, string(decodedMessage))
			}
		}
	}()

	// wait for a short time to collect all instructions
	time.Sleep(200 * time.Millisecond)

	for _, instruction := range instructions {
		vm.execute(instruction)
	}

	sum := vm.sumRegisters()

	result := fmt.Sprintf("%d", sum)
	encodedResult := base64.StdEncoding.EncodeToString([]byte(result))

	err = c.WriteMessage(websocket.TextMessage, []byte(encodedResult))
	if err != nil {
		log.Println("write:", err)
		return
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			err := c.WriteMessage(websocket.PingMessage, nil)
			if err != nil {
				log.Println("write:", err)
				return
			}
		case <-interrupt:
			log.Println("interrupt")
			err := c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				log.Println("write close:", err)
			}
			return
		}
	}
}

func refreshSession() error {
	resp, err := client.Get("https://challenge.longshotsystems.co.uk/go")
	if err != nil {
		return fmt.Errorf("failed to refresh session: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	log.Printf("Session refresh response: %s", string(body))

	return nil
}

type VirtualMachine struct {
	registers [16]int64
}

func (vm *VirtualMachine) execute(instruction string) {
	parts := strings.Split(instruction, " ")
	switch parts[0] {
	case "ADD":
		value, _ := strconv.ParseInt(parts[1], 10, 64)
		src, _ := strconv.Atoi(parts[2][1:])
		dst, _ := strconv.Atoi(parts[3][1:])
		vm.registers[dst] = vm.registers[src] + value
	case "MOV":
		src, _ := strconv.Atoi(parts[1][1:])
		dst, _ := strconv.Atoi(parts[2][1:])
		vm.registers[dst] = vm.registers[src]
	case "STORE":
		value, _ := strconv.ParseInt(parts[1], 10, 64)
		dst, _ := strconv.Atoi(parts[2][1:])
		vm.registers[dst] = value
	}
}

func (vm *VirtualMachine) sumRegisters() int64 {
	var sum int64

	for _, v := range vm.registers {
		sum += v
	}

	return sum
}
