package main

import (
	"bytes"
	"fmt"
	"github.com/emersion/go-message"
	"github.com/joho/godotenv"
	"github.com/knadh/go-pop3"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	email := os.Getenv("EMAIL")
	password := os.Getenv("PASSWORD")
	pop3Server := os.Getenv("POP3_SERVER")
	pop3PortStr := os.Getenv("POP3_PORT")

	pop3Port, err := strconv.Atoi(pop3PortStr)
	if err != nil {
		log.Fatalf("Invalid POP3_PORT: %v", err)
	}

	// Initialize the client.
	p := pop3.New(pop3.Opt{
		Host:       pop3Server,
		Port:       pop3Port,
		TLSEnabled: false,
	})

	// Create a new connection. POP3 connections are stateful and should end
	// with a Quit() once the opreations are done.
	c, err := p.NewConn()
	if err != nil {
		log.Fatal(err)
	}
	defer c.Quit()

	// Authenticate.
	if err := c.Auth(email, password); err != nil {
		log.Fatal(err)
	}

	// Print the total number of messages and their size.d
	count, size, _ := c.Stat()
	fmt.Println("total messages=", count, "size=", size)

	// Pull the list of all message IDs and their sizes
	msgs, _ := c.List(0)

	mailDir := "backup"
	logDir := "logs"

	os.MkdirAll(mailDir, 0755)
	os.MkdirAll(logDir, 0755)

	logFilePath := filepath.Join(logDir, "mail.log")
	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("❌ Failed to create log file: %v", err)
	}
	defer logFile.Close()
	log.SetOutput(logFile)
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	for _, msg := range msgs {
		fmt.Printf("📨 Processing message ID: %d\n", msg.ID)

		entity, err := c.Retr(msg.ID)
		if err != nil {
			log.Printf("❌ Failed to retrieve message ID %d: %v", msg.ID, err)
			continue
		}

		// Save raw .eml
		emlPath := filepath.Join(mailDir, fmt.Sprintf("mail_%d.eml", msg.ID))
		f, err := os.Create(emlPath)
		if err != nil {
			log.Printf("❌ Failed to save message ID %d: %v", msg.ID, err)
			continue
		}
		if err := entity.WriteTo(f); err != nil {
			log.Printf("❌ Failed to write message ID %d to file: %v", msg.ID, err)
		}
		f.Close()

		// Log Content-Type after saving .eml
		contentType, params, err := entity.Header.ContentType()
		if err != nil {
			log.Printf("⚠️ Failed to parse Content-Type for message %d: %v", msg.ID, err)
		} else {
			log.Printf("📌 mail_%d Content-Type: %s; boundary=%s", msg.ID, contentType, params["boundary"])
		}

		// Log header info
		fmt.Println("📨 Subject:", decodeMIMEHeader(entity.Header.Get("Subject")))
		fmt.Println("📬 From:", decodeMIMEHeader(entity.Header.Get("From")))
		fmt.Println("📅 Date:", entity.Header.Get("Date"))

		// Full header dump
		fmt.Println("🧾 All Headers:")
		decoder := new(mime.WordDecoder)
		fields := entity.Header.Fields()
		for fields.Next() {
			key := fields.Key()
			value := fields.Value()
			decoded, err := decoder.DecodeHeader(value)
			if err != nil {
				decoded = "[Decode Error] " + value
			}
			fmt.Printf("  %s: %s\n", key, decoded)
		}

		// Save full MIME body (entity) for debug
		rawBodyPath := filepath.Join(mailDir, fmt.Sprintf("mail_%d_rawbody.txt", msg.ID))
		var buf bytes.Buffer
		if err := entity.WriteTo(&buf); err != nil {
			log.Printf("❌ Failed to buffer entity for message %d: %v", msg.ID, err)
		} else {
			if err := os.WriteFile(rawBodyPath, buf.Bytes(), 0644); err != nil {
				log.Printf("⚠️ Failed to write raw body file for message %d: %v", msg.ID, err)
			}
		}

		// Body
		body := extractPlainText(entity)
		log.Printf("📝 mail_%d body result: %s", msg.ID, summarizeBodyPreview(body))
		fmt.Println("📄 Body:\n" + body)
		fmt.Println("====================================\n")
	}

}
func extractPlainText(e *message.Entity) string {
	mt, params, _ := mime.ParseMediaType(e.Header.Get("Content-Type"))

	if strings.HasPrefix(mt, "multipart/") {
		boundary := params["boundary"]
		mr := multipart.NewReader(e.Body, boundary)
		parts := []*multipart.Part{}
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			} else if err != nil {
				log.Printf("⚠️ Failed to read multipart: %v", err)
				break
			}

			parts = append(parts, p)
		}

		for _, p := range parts {
			partType, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
			b, err := io.ReadAll(p)
			if err != nil {
				log.Printf("⚠️ Failed to read part body: %v", err)
				continue
			}
			if partType == "text/plain" {
				return string(b)
			}
			if partType == "text/html" {
				return "[HTML] " + string(b)
			}
		}
		log.Printf("⚠️ No usable part (text/plain or text/html) found in multipart message.")
		return "[Multipart: No plain or HTML body detected]"
	} else if mt == "text/plain" || mt == "text/html" {
		b, err := io.ReadAll(e.Body)
		if err != nil {
			log.Printf("⚠️ Failed to read entity body: %v", err)
			return "[Body Read Error]"
		}
		htmlStr := string(b)
		if mt == "text/html" {
			if strings.Contains(htmlStr, "<img") && !strings.Contains(htmlStr, "<p>") {
				imgs := extractImageSrcs(htmlStr)
				if len(imgs) > 0 {
					return "[이미지 기반 메일입니다]\n이미지 URL:\n" + strings.Join(imgs, "\n")
				}
				return "[이미지 기반 본문입니다. GUI에서 확인해주세요]"
			}
			return "[HTML] " + htmlStr
		}
		return htmlStr
	}
	return "[No Body]"
}

func decodeMIMEHeader(s string) string {
	decoded, err := (&mime.WordDecoder{}).DecodeHeader(s)
	if err != nil {
		return s
	}
	return decoded
}

func extractImageSrcs(html string) []string {
	var urls []string
	start := 0
	for {
		imgIndex := strings.Index(html[start:], "<img")
		if imgIndex == -1 {
			break
		}
		imgStart := start + imgIndex
		srcIndex := strings.Index(html[imgStart:], "src=\"")
		if srcIndex == -1 {
			break
		}
		srcStart := imgStart + srcIndex + len("src=\"")
		srcEnd := strings.Index(html[srcStart:], "\"")
		if srcEnd == -1 {
			break
		}
		url := html[srcStart : srcStart+srcEnd]
		urls = append(urls, url)
		start = srcStart + srcEnd
	}
	return urls
}

// summarizeBodyPreview returns a short tag for the type of body content.
func summarizeBodyPreview(s string) string {
	if len(s) == 0 {
		return "[EMPTY]"
	}
	if strings.HasPrefix(s, "[HTML]") {
		return "[HTML]"
	}
	if strings.HasPrefix(s, "[이미지 기반") {
		return "[IMG-ONLY]"
	}
	if strings.HasPrefix(s, "[Multipart") {
		return "[MULTIPART]"
	}
	return "[PLAIN]"
}
