package recording

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultFPS     = 15
	frameQueueSize = 120
)

// recorder streams microscope JPEG frames to ffmpeg and lets ffmpeg produce a common
// H.264 MP4 file. Frames are non blocking and dropped rather than blocking the live view.
type recorder struct {
	stdin  io.WriteCloser
	cmd    *exec.Cmd
	done   chan error
	frames chan []byte
	path   string
	errBuf bytes.Buffer
}

// Manager owns the app's current recorder pointer and serializes access to it.
type Manager struct {
	recorder *recorder
	mu       sync.Mutex
}

// Active reports whether a recording is currently running.
func (m *Manager) Active() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recorder != nil
}

// Start opens ffmpeg and starts recording to path.
func (m *Manager) Start(path string) error {
	recorder, err := start(path)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.recorder != nil {
		m.mu.Unlock()
		_ = recorder.finish()
		return errors.New("recording is already active")
	}
	m.recorder = recorder
	m.mu.Unlock()
	return nil
}

// WriteFrame queues one JPEG frame on the active recorder, if any.
func (m *Manager) WriteFrame(jpeg []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recorder != nil {
		m.recorder.writeFrame(jpeg)
	}
}

// Finish finalizes the active recording and returns its MP4 path. ok is false
// when no recording was active.
func (m *Manager) Finish() (path string, ok bool, err error) {
	m.mu.Lock()
	recorder := m.recorder
	m.recorder = nil
	m.mu.Unlock()
	if recorder == nil {
		return "", false, nil
	}
	path = recorder.outputPath()
	return path, true, recorder.finish()
}

func start(path string) (*recorder, error) {
	ffmpeg, err := findFFmpeg()
	if err != nil {
		return nil, err
	}
	encoder, err := findH264Encoder(ffmpeg)
	if err != nil {
		return nil, err
	}

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-f", "mjpeg",
		"-framerate", fmt.Sprintf("%d", defaultFPS),
		"-i", "pipe:0",
		"-an",
		"-c:v", encoder,
	}
	if encoder == "libx264" {
		args = append(args, "-preset", "veryfast")
	}
	args = append(args,
		"-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
		path,
	)

	cmd := exec.Command(ffmpeg, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open ffmpeg stdin: %w", err)
	}

	r := &recorder{
		path:   path,
		cmd:    cmd,
		stdin:  stdin,
		done:   make(chan error, 1),
		frames: make(chan []byte, frameQueueSize),
	}
	cmd.Stderr = &r.errBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}

	go r.writeLoop()
	return r, nil
}

func (r *recorder) outputPath() string {
	return r.path
}

func (r *recorder) writeFrame(jpeg []byte) {
	if len(jpeg) == 0 {
		return
	}
	frame := append([]byte(nil), jpeg...)
	select {
	case r.frames <- frame:
	default:
	}
}

// single-use: Manager.Finish removes the recorder before finalizing it.
func (r *recorder) finish() error {
	close(r.frames)
	return <-r.done
}

func (r *recorder) writeLoop() {
	var writeErr error
	for frame := range r.frames {
		if _, err := r.stdin.Write(frame); err != nil {
			writeErr = err
			break
		}
	}
	_ = r.stdin.Close()
	waitErr := r.cmd.Wait()
	if writeErr != nil {
		r.done <- fmt.Errorf("write video frame: %w", writeErr)
		return
	}
	if waitErr != nil {
		msg := r.errBuf.String()
		if msg != "" {
			r.done <- fmt.Errorf("ffmpeg failed: %w\n%s", waitErr, msg)
			return
		}
		r.done <- fmt.Errorf("ffmpeg failed: %w", waitErr)
		return
	}
	r.done <- nil
}

// findFFmpeg returns a verified ffmpeg executable path. It checks an explicit
// environment override, common bundled locations, then the user's PATH.
func findFFmpeg() (string, error) {
	candidates := make([]string, 0, 8)
	if env := os.Getenv("MICROVIEW_FFMPEG"); env != "" {
		candidates = append(candidates, env)
	}
	candidates = append(candidates, "./ffmpeg")
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "ffmpeg"),
			filepath.Join(dir, "..", "Resources", "ffmpeg"),
		)
	}
	candidates = append(candidates,
		"/opt/homebrew/bin/ffmpeg",
		"/usr/local/bin/ffmpeg",
		"/usr/bin/ffmpeg",
	)
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		candidates = append(candidates, path)
	}

	for _, candidate := range candidates {
		if verifyFFmpeg(candidate) == nil {
			abs, err := filepath.Abs(candidate)
			if err == nil {
				return abs, nil
			}
			return candidate, nil
		}
	}
	return "", errors.New("ffmpeg was not found. Install ffmpeg, put an ffmpeg binary next to MicroView, or set MICROVIEW_FFMPEG to the path of your ffmpeg installation")
}

func verifyFFmpeg(path string) error {
	resolved, err := exec.LookPath(path)
	if err != nil {
		return err
	}
	cmd := exec.Command(resolved, "-version")
	return cmd.Run()
}

func findH264Encoder(ffmpeg string) (string, error) {
	out, err := exec.Command(ffmpeg, "-hide_banner", "-encoders").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("inspect ffmpeg encoders: %w", err)
	}
	encoders := string(out)
	for _, encoder := range []string{"libx264", "h264_videotoolbox"} {
		if strings.Contains(encoders, encoder) {
			return encoder, nil
		}
	}
	return "", errors.New("ffmpeg does not include a supported H.264 encoder (libx264 or h264_videotoolbox)")
}

// PosterPath returns the sidecar thumbnail path for an MP4 recording.
func PosterPath(videoPath string) string {
	ext := filepath.Ext(videoPath)
	return strings.TrimSuffix(videoPath, ext) + ".poster.jpg"
}

// WritePoster extracts a single representative frame from an MP4 recording.
func WritePoster(videoPath string) (string, error) {
	ffmpeg, err := findFFmpeg()
	if err != nil {
		return "", err
	}
	posterPath := PosterPath(videoPath)
	cmd := exec.Command(
		ffmpeg,
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", videoPath,
		"-frames:v", "1",
		"-q:v", "2",
		posterPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 0 {
			return "", fmt.Errorf("create video poster: %w\n%s", err, string(out))
		}
		return "", fmt.Errorf("create video poster: %w", err)
	}
	return posterPath, nil
}

// Name returns a timestamped MP4 filename.
func Name(t time.Time) string {
	return fmt.Sprintf("microview-%s.mp4", t.Format("20060102-150405"))
}
