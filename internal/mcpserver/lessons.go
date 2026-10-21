package mcpserver

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Lessons are short notes an agent keeps per app about what made it slow and
// what worked, so the next task in that app plans better. They live on this
// Mac only, outside the plugin and the source checkout, which updates replace.

const maxLessons = 40

func lessonsDir() string {
	if d := os.Getenv("VNKVM_LESSONS_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "vnkvm", "lessons")
}

var unsafeName = regexp.MustCompile(`[^a-z0-9]+`)

func lessonsFile(app string) (string, error) {
	name := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(app), "-"), "-")
	if name == "" {
		return "", fmt.Errorf("give the app name, e.g. Safari")
	}
	return filepath.Join(lessonsDir(), name+".md"), nil
}

func readLessons(app string) ([]string, error) {
	path, err := lessonsFile(app)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(strings.TrimPrefix(l, "- ")); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out, err
}

// writeLessons keeps the newest maxLessons lines.
func writeLessons(app string, lines []string) error {
	path, err := lessonsFile(app)
	if err != nil {
		return err
	}
	if len(lines) > maxLessons {
		lines = lines[len(lines)-maxLessons:]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# vnkvm lessons: %s\n\n", app)
	for _, l := range lines {
		b.WriteString("- " + l + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func addLesson(app, text string) ([]string, error) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return nil, fmt.Errorf("the lesson is empty")
	}
	if len(text) > 300 {
		return nil, fmt.Errorf("keep a lesson under 300 characters")
	}
	lines, err := readLessons(app)
	if err != nil {
		return nil, err
	}
	for _, l := range lines {
		if strings.Contains(strings.ToLower(l), strings.ToLower(text)) {
			return lines, nil // already known
		}
	}
	lines = append(lines, time.Now().Format("2006-01-02")+": "+text)
	return lines, writeLessons(app, lines)
}

// stats counts what this server process has done, to spot an inefficient
// loop the model cannot see from inside one turn.
type stats struct {
	mu sync.Mutex
	counters
	lookStreak    int // screenshots since the last action
	singleStreak  int // single-action calls in a row
	lastFail      string
	lastFailCount int
}

type counters struct {
	Shots    int            `json:"shots"`
	Zooms    int            `json:"zooms"`
	Acts     int            `json:"act_calls"`
	Singles  int            `json:"single_action_calls"`
	Steps    int            `json:"steps_run"`
	Failures map[string]int `json:"failures"`
}

func (s *stats) shot(zoom bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Shots++
	if zoom {
		s.Zooms++
	}
	s.lookStreak++
	if s.lookStreak == 3 {
		return "Efficiency: 3 screenshots without an action. Plan from the shot you have, and zoom only where a target is unclear."
	}
	return ""
}

func (s *stats) action(single bool, steps int, err error) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookStreak = 0
	s.Steps += steps
	var hints []string
	if single {
		s.Singles++
		s.singleStreak++
		if s.singleStreak == 3 {
			hints = append(hints, "Efficiency: 3 single actions in a row. Chain the steps you can already see into one act call.")
		}
	} else {
		s.Acts++
		s.singleStreak = 0
	}
	if err != nil {
		kind := failureKind(err)
		if s.Failures == nil {
			s.Failures = map[string]int{}
		}
		s.Failures[kind]++
		if kind == s.lastFail {
			s.lastFailCount++
		} else {
			s.lastFail, s.lastFailCount = kind, 1
		}
		if s.lastFailCount == 2 && kind != "user took control" {
			hints = append(hints, fmt.Sprintf("Efficiency: the same failure (%s) twice. Change the approach, and record what works with the lessons tool.", kind))
		}
	}
	return strings.Join(hints, "\n")
}

func (s *stats) snapshot() counters {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.counters
	c.Failures = map[string]int{}
	for k, v := range s.Failures {
		c.Failures[k] = v
	}
	return c
}

func failureKind(err error) string {
	msg := err.Error()
	for _, k := range []string{"user took control", "covered", "changed size", "is gone", "outside", "unknown shot"} {
		if strings.Contains(msg, k) {
			return k
		}
	}
	return "other"
}
