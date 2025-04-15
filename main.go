package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"net/http"
	"encoding/base64"
	"bufio"
	"io"
	"github.com/joho/godotenv"
	"bytes"
	"time"
	"strings"
	"github.com/araddon/dateparse"
	"net/smtp"

	"google.golang.org/api/calendar/v3"
	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

func main() {
	for {
		checkInbox()
		time.Sleep(10 * time.Second)
		log.Println("Checking inbox...")
	}
}

type CalendarEvent struct {
	Title       string `json:"title"`
	StartTime   string `json:"start_time"`
	EndTime     string `json:"end_time"`
	Location    string `json:"location"`
	Description string `json:"description"`
}


func loadProcessedIDs(filename string) map[string]bool {
	processed := make(map[string]bool)

	file, err := os.Open(filename)
	if err != nil {
		return processed // file might not exist yet
	}
	defer file.Close()

	var id string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		id = scanner.Text()
		processed[id] = true
	}
	return processed
}

func checkInbox() {
	ctx := context.Background()

	b, err := os.ReadFile("credentials.json")
	if err != nil {
		log.Fatalf("Unable to read client secret file: %v", err)
	}

	config, err := google.ConfigFromJSON(b,
		calendar.CalendarEventsScope,
		gmail.GmailReadonlyScope,
	)
	if err != nil {
		log.Fatalf("Unable to parse client secret file to config: %v", err)
	}

	client := getClient(config)
	srv, err := gmail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Gmail client: %v", err)
	}

	user := "me"
	req := srv.Users.Messages.
		List(user).
		LabelIds("INBOX").
		Q("from:alex.boden@uwaterloo.ca OR from:alexboden13@gmail.com").
		MaxResults(10)
	res, err := req.Do()
	if err != nil {
		log.Fatalf("Unable to retrieve messages: %v", err)
	}
	processed := loadProcessedIDs("processed_ids.txt")
	
	f, err := os.OpenFile("processed_ids.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Unable to open processed_ids.txt: %v", err)
	}
	defer f.Close()

	for _, m := range res.Messages {
		if processed[m.Id] {
			continue // skip already seen
		}

		msg, err := srv.Users.Messages.Get(user, m.Id).Do()

		emailBody := extractEmailBody(msg)
		log.Printf("Processing message ID: %s", m.Id)
		log.Printf("Email Body: %s", emailBody)
		if strings.Contains(emailBody, "Email Body: Alex Boden has declined this invitation.") {
			log.Printf("Skipping declined invitation for message ID: %s", m.Id)
			log.Printf("Skipping declined invitation for message ID: %s", m.Id)
			continue
		}

		gptResponse, err := createCalendarSummary(emailBody)
		if err != nil {
			log.Printf("OpenAI request failed: %v", err)
		} else {
			fmt.Println("📅 Suggested Calendar Event:\n", gptResponse)
		}

		var event CalendarEvent
		err = json.Unmarshal([]byte(gptResponse), &event)
		if err != nil {
			log.Fatalf("Failed to parse GPT response: %v", err)
		}

		parsedStartTime, err := parseToEastern(event.StartTime)
		if err != nil {
			log.Fatalf("❌ Couldn't parse start_time: %v", err)
		}
		event.StartTime = parsedStartTime.Format(time.RFC3339)

		if event.EndTime == "" {
			// default to 1-hour event
			start, err := dateparse.ParseLocal(event.StartTime)
			if err == nil {
				event.EndTime = start.Add(1 * time.Hour).Format(time.RFC3339)
				event.StartTime = start.Format(time.RFC3339)
			}
		}

		parsedEndTime, err := parseToEastern(event.EndTime)

		if err != nil {
			log.Fatalf("❌ Couldn't parse end_time: %v", err)
		}
		event.EndTime = parsedEndTime.Format(time.RFC3339)

		if event.Location == "" {
			event.Location = "Virtual"
		}
		if event.Description == "" {
			event.Description = "No description provided."
		}
		if event.Title == "" {
			event.Title = "No title provided."
		}

		err = createGoogleCalendarEvent(event)
		if err != nil {
			log.Fatalf("Failed to create calendar event: %v", err)
		}

		if _, err := f.WriteString(m.Id + "\n"); err != nil {
			log.Printf("Failed to save processed ID: %v", err)
		}
	}
}

func extractEmailBody(msg *gmail.Message) string {
	// Try direct body first (rare)
	if msg.Payload.Body != nil && msg.Payload.Body.Data != "" {
		return decodeBody(msg.Payload.Body.Data)
	}

	// Walk MIME parts
	return traverseParts(msg.Payload.Parts)
}

func traverseParts(parts []*gmail.MessagePart) string {
	for _, part := range parts {
		if part.MimeType == "text/plain" && part.Body != nil && part.Body.Data != "" {
			return decodeBody(part.Body.Data)
		}
		// Recursively check nested parts
		if len(part.Parts) > 0 {
			body := traverseParts(part.Parts)
			if body != "" {
				return body
			}
		}
	}
	return "(no body found)"
}

func decodeBody(data string) string {
	decoded, err := base64.URLEncoding.DecodeString(data)
	if err != nil {
		// fallback: some emails use standard base64
		decoded, err = base64.StdEncoding.DecodeString(data)
		if err != nil {
			return "(failed to decode body)"
		}
	}
	return string(decoded)
}


// getClient gets a token, saves it, and returns an HTTP client
func getClient(config *oauth2.Config) *http.Client {
	const tokenFile = "token.json"
	tok, err := tokenFromFile(tokenFile)
	if err != nil {
		tok = getTokenFromWeb(config)
		saveToken(tokenFile, tok)
	}
	return config.Client(context.Background(), tok)
}

func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	authCodeChan := make(chan string)

	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			fmt.Fprintf(w, "Error: No code received")
			return
		}
		fmt.Fprintf(w, "Authorization successful! You can close this window.")
		authCodeChan <- code
	})

	go func() {
		log.Printf("Starting server at http://localhost:8080/callback")
		log.Fatal(http.ListenAndServe(":8080", nil))
	}()

	config.RedirectURL = "http://localhost:8080/callback"
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline, oauth2.ApprovalForce)

	fmt.Printf("Visit this URL in your browser to authorize:\n%v\n", authURL)

	code := <-authCodeChan

	tok, err := config.Exchange(context.Background(), code)
	if err != nil {
		log.Fatalf("Unable to retrieve token from web: %v", err)
	}

	if tok.RefreshToken == "" {
		log.Fatalf("No refresh token received. Try removing token.json and authenticating again.")
	}

	return tok
}

func saveToken(path string, token *oauth2.Token) {
	fmt.Printf("Saving credential file to: %s\n", path)
	f, err := os.Create(path)
	if err != nil {
		log.Fatalf("Unable to cache oauth token: %v", err)
	}
	defer f.Close()
	json.NewEncoder(f).Encode(token)
}

func buildCalendarPrompt(emailBody string) string {
	loc, _ := time.LoadLocation("America/Toronto") // EST/EDT
	now := time.Now().In(loc).Format("Mon Jan 2 15:04:05 MST 2006")

	return fmt.Sprintf(`
You are a helpful assistant that extracts calendar event details from emails.

📅 Current date and time is: %s (in America/Toronto timezone).

🎯 Your task is to extract ONLY the necessary fields to create a Google Calendar event.

✅ Please return a **valid JSON object** with these keys:
- title: Short summary of the event
- start_time: Exact start time (in natural language or ISO format)
- end_time: Optional end time
- location: Optional physical or virtual location
- description: Optional notes, body, or context

Only include fields that are clearly mentioned in the email.

📨 Email Body:
%s
`, now, emailBody)
}

func createCalendarSummary(emailBody string) (string, error) {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		log.Printf("Warning: Error loading .env file: %v", err)
	}
	
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY not set")
	}
	prompt := buildCalendarPrompt(emailBody)

	requestBody := map[string]interface{}{
		"model": "gpt-4", // or "gpt-3.5-turbo"
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make OpenAI API call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("OpenAI API error: %s", string(body))
	}

	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	err = json.NewDecoder(resp.Body).Decode(&response)
	if err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if len(response.Choices) == 0 {
		return "", fmt.Errorf("no response choices")
	}

	return response.Choices[0].Message.Content, nil
}

func createGoogleCalendarEvent(event CalendarEvent) error {
	ctx := context.Background()

	b, err := os.ReadFile("credentials.json")
	if err != nil {
		return err
	}
	config, err := google.ConfigFromJSON(b,
		calendar.CalendarEventsScope,
		gmail.GmailReadonlyScope,
	)
	if err != nil {
		return err
	}

	client := getClient(config)

	srv, err := calendar.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return err
	}

	calendarEvent := &calendar.Event{
		Summary:     event.Title,
		Location:    event.Location,
		Description: event.Description,
		Start: &calendar.EventDateTime{
			DateTime: event.StartTime,
			TimeZone:  "America/New_York",
		},
		End: &calendar.EventDateTime{
			DateTime: event.EndTime,
			TimeZone:  "America/New_York",
		},
		Attendees: []*calendar.EventAttendee{
			{
				Email: "alexboden13@gmail.com",
			},
			{
				Email: "intern.email.bot@gmail.com", 
			},
		},
	}

	createdEvent, err := srv.Events.Insert("primary", calendarEvent).Do()
	if err != nil {
		return err
	}

	log.Printf("✅ Event created: %s", createdEvent.HtmlLink)
	eventJSON, _ := json.MarshalIndent(calendarEvent, "", "  ")
	emailBody := fmt.Sprintf("Calendar event created! View it here: %s\n\nEvent details:\n%s", createdEvent.HtmlLink, string(eventJSON))

	// Load email config from env
	from := os.Getenv("EMAIL_FROM")
	password := os.Getenv("EMAIL_PASSWORD")
	to := "alexboden13@gmail.com"

	// Compose email
	msg := []byte(fmt.Sprintf("To: %s\r\n"+
		"Subject: Calendar Event Created\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n"+
		"\r\n"+
		"%s", to, emailBody))

	// Send email
	smtpHost := "smtp.gmail.com"
	smtpPort := "587"
	auth := smtp.PlainAuth("", from, password, smtpHost)
	err = smtp.SendMail(smtpHost+":"+smtpPort, auth, from, []string{to}, msg)

	if err != nil {
		log.Printf("Failed to send email: %v", err)
	}
	return nil
}

func parseToEastern(t string) (time.Time, error) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.Time{}, err
	}
	parsed, err := dateparse.ParseIn(t, loc)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.In(loc), nil
}