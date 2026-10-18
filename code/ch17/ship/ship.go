// Package ship sends remind's crash reports to the triage service by
// following the crash file remind already writes, as a log shipper
// does: remind is unchanged, and nothing it does waits for the network
// (chapter 17).
package ship

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// maxLine is the longest report line sent; the service refuses longer
// ones anyway (its own limit is the same).
const maxLine = 256 << 10

// headLen is how much of the file's start identifies it: a file
// rotated or replaced starts differently, and is read from the start.
const headLen = 512

// ErrRefused means the service refused the shipper itself, such as for
// a wrong secret: sending more reports can't help.
var ErrRefused = errors.New("ship: the service refused the shipper")

// tag::shipper[]

// A Shipper follows one crash file and posts each complete line to
// the service, in order, one at a time. How far it has sent is saved
// in Offset after each post, so a restart goes on from there; a crash
// between a post and the save sends that report again, which the
// service recognizes and ignores.
type Shipper struct {
	File   string // remind's crash file, one JSON report per line
	Offset string // where the shipper saves how far it has sent
	URL    string // the service's POST /crashes
	Secret string
	Client *http.Client  // default: a 10 s timeout
	Poll   time.Duration // how often the file is checked; default 1 s
	Log    *slog.Logger

	base time.Duration // the first wait after a failure (tests: 1 ms)
}

// end::shipper[]

// A position is how far the shipper has sent: a byte offset, and a
// hash of the file's first bytes (up to headLen, and never past the
// offset), which tells a new file from the old one.
type position struct {
	Offset  int64  `json:"offset"`
	HeadLen int    `json:"head_len"`
	Head    string `json:"head"`
}

// tag::run[]

// Run sends what the file holds and then what is added to it, until
// ctx ends. It returns ctx's cause then, or ErrRefused if the service
// refuses the shipper itself.
func (s *Shipper) Run(ctx context.Context) error {
	poll := cmpDur(s.Poll, time.Second)
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		if err := s.drain(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("ship: %w", context.Cause(ctx))
		case <-t.C:
		}
	}
}

// end::run[]

// drain sends every complete line after the saved position. A line
// still being written, with no newline yet, waits for the next poll.
func (s *Shipper) drain(ctx context.Context) error {
	f, err := os.Open(s.File)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // no crash yet
	}
	if err != nil {
		return fmt.Errorf("ship: %w", err)
	}
	defer f.Close() // read only

	// tag::drain[]

	pos, err := s.start(f)
	if err != nil {
		return err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := readLine(r)
		if errors.Is(err, io.EOF) {
			return nil // a partial line stays for the next poll
		}
		if err != nil {
			return fmt.Errorf("ship: %w", err)
		}
		if err := s.send(ctx, line); err != nil {
			return err
		}
		pos.Offset += int64(len(line))
		if err := s.save(f, pos); err != nil { // <1>
			return err
		}
	}

	// end::drain[]
}

// tag::start[]

// start returns where to begin in f: the saved position, unless the
// file is shorter than it or starts differently, as after a rotation
// or a truncation, when the shipper starts over from the beginning.
func (s *Shipper) start(f *os.File) (position, error) {
	var saved position
	if err := readPos(s.Offset, &saved); err != nil {
		return position{}, err
	}
	fi, err := f.Stat()
	if err != nil {
		return position{}, fmt.Errorf("ship: %w", err)
	}
	head, err := headOf(f, saved.HeadLen)
	if err != nil {
		return position{}, err
	}
	var cur position
	if fi.Size() >= saved.Offset && hashOf(head) == saved.Head {
		cur = saved
	}
	if _, err := f.Seek(cur.Offset, io.SeekStart); err != nil {
		return position{}, fmt.Errorf("ship: %w", err)
	}
	return cur, nil
}

// end::start[]

// readLine returns the next line with its newline, or io.EOF if no
// complete line is left. A line longer than maxLine is returned whole
// so its length is counted, and send skips it.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		switch {
		case err == nil:
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return nil, io.EOF
		default:
			return nil, err //nolint:wrapcheck // drain wraps it
		}
	}
}

// tag::send[]

// send posts one line, waiting and trying again while the service is
// unreachable or asks for time (429, 503), until it answers or ctx
// ends. A line the service rejects as a report (400, 413), or one too
// long to send, is logged and skipped, so one bad line can't stop the
// rest.
func (s *Shipper) send(ctx context.Context, line []byte) error {
	body := bytes.TrimSpace(line)
	if len(body) == 0 {
		return nil
	}
	if len(body) > maxLine || !jsontext.Value(body).IsValid() {
		s.log().Warn("skipped a line that isn't a report",
			"bytes", len(body))
		return nil
	}

	// end::send[]

	// tag::send-retry[]

	for try := 0; ; try++ {
		status, wait, err := s.post(ctx, body)
		switch {
		case err == nil && status < 300:
			return nil
		case status == http.StatusUnauthorized,
			status == http.StatusForbidden:
			return fmt.Errorf("%w: %d", ErrRefused, status)
		case err == nil && status != http.StatusTooManyRequests &&
			status != http.StatusServiceUnavailable && status < 500:
			s.log().Warn("the service rejected a report",
				"status", status)
			return nil
		}
		if wait <= 0 {
			wait = min(s.first()<<min(try, 6), time.Minute)
		}
		s.log().Info("waiting to send", "status", status, "err", err,
			"wait", wait)
		select {
		case <-ctx.Done():
			return fmt.Errorf("ship: %w", context.Cause(ctx))
		case <-time.After(min(wait, 5*time.Minute)):
		}
	}
}

// end::send-retry[]

func (s *Shipper) post(ctx context.Context, body []byte) (int,
	time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL,
		bytes.NewReader(body))
	if err != nil {
		return 0, 0, fmt.Errorf("ship: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Secret)
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("ship: %w", err)
	}
	defer resp.Body.Close()
	// Read what is left, so the connection can be used again.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	secs, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
	return resp.StatusCode, time.Duration(secs) * time.Second, nil
}

// headOf returns the first n bytes of f, or fewer if f is shorter.
func headOf(f *os.File, n int) ([]byte, error) {
	head := make([]byte, min(n, headLen))
	k, err := f.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("ship: %w", err)
	}
	return head[:k], nil
}

// save writes the position as chapter 15 writes a checkpoint: a
// temporary file, synced, renamed over the old one. The head it saves
// covers bytes already sent, which an appended file never changes.
func (s *Shipper) save(f *os.File, p position) error {
	head, err := headOf(f, int(min(p.Offset, headLen)))
	if err != nil {
		return err
	}
	p.HeadLen, p.Head = len(head), hashOf(head)
	b, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("ship: save position: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Offset), ".offset-*")
	if err != nil {
		return fmt.Errorf("ship: save position: %w", err)
	}
	defer os.Remove(tmp.Name()) // fails once renamed, as intended
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), s.Offset)
	}
	if err != nil {
		return fmt.Errorf("ship: save position: %w", err)
	}
	return nil
}

func readPos(file string, p *position) error {
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // nothing sent yet
	}
	if err != nil {
		return fmt.Errorf("ship: read position: %w", err)
	}
	if err := json.Unmarshal(b, p); err != nil {
		return fmt.Errorf("ship: read position: %w", err)
	}
	return nil
}

func hashOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s *Shipper) first() time.Duration {
	return cmpDur(s.base, time.Second)
}

func (s *Shipper) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

func cmpDur(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}
