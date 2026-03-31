// Package ffmpeg provides a wrapper around the FFmpeg CLI to produce raw video
// frames that can be consumed by a Go application.
//
// It launches an ffmpeg sub-process that generates (or re-encodes) video and
// outputs raw RGBA pixel data on stdout, which the caller reads frame-by-frame.
package ffmpeg

import (
	"fmt"
	"io"
	"os/exec"
)

// Stream represents a running FFmpeg process that produces raw video frames.
type Stream struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	label  string
	width  int
	height int
}

// Config holds the settings used to launch an FFmpeg stream.
type Config struct {
	// Label is a human-readable name for this stream (e.g. "SD", "HD").
	Label string
	// Width and Height of the output frames in pixels.
	Width  int
	Height int
	// FPS is the target frame rate.
	FPS int
	// Input is the FFmpeg input specifier.  Use "testsrc" to generate a
	// built-in test pattern (no file required), or pass a file path / URL.
	Input string
}

// SDConfig returns a 640x480 @ 30 fps SD configuration.
func SDConfig(input string) Config {
	return Config{
		Label:  "SD",
		Width:  640,
		Height: 480,
		FPS:    30,
		Input:  input,
	}
}

// HDConfig returns a 1280x720 @ 30 fps HD configuration.
func HDConfig(input string) Config {
	return Config{
		Label:  "HD",
		Width:  1280,
		Height: 720,
		FPS:    30,
		Input:  input,
	}
}

// Start launches an FFmpeg process according to cfg and returns a *Stream
// whose Read method yields raw RGBA frames on stdout.
func Start(cfg Config) (*Stream, error) {
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.FPS <= 0 {
		return nil, fmt.Errorf("ffmpeg: invalid config: width=%d height=%d fps=%d",
			cfg.Width, cfg.Height, cfg.FPS)
	}

	args := buildArgs(cfg)
	cmd := exec.Command("ffmpeg", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg: stdout pipe: %w", err)
	}

	// Discard stderr so ffmpeg's log output doesn't block.
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg: start: %w", err)
	}

	return &Stream{
		cmd:    cmd,
		stdout: stdout,
		label:  cfg.Label,
		width:  cfg.Width,
		height: cfg.Height,
	}, nil
}

// ReadFrame reads exactly one RGBA frame (width*height*4 bytes) from FFmpeg's
// stdout into buf.  buf must be at least FrameSize() bytes long.
// Returns io.EOF when the stream ends.
func (s *Stream) ReadFrame(buf []byte) error {
	need := s.FrameSize()
	if len(buf) < need {
		return fmt.Errorf("ffmpeg: buffer too small: need %d, got %d", need, len(buf))
	}
	_, err := io.ReadFull(s.stdout, buf[:need])
	return err
}

// FrameSize returns the number of bytes in a single RGBA frame.
func (s *Stream) FrameSize() int {
	return s.width * s.height * 4 // RGBA → 4 bytes per pixel
}

// Label returns the human-readable name of this stream (e.g. "SD", "HD").
func (s *Stream) Label() string { return s.label }

// Width returns the frame width in pixels.
func (s *Stream) Width() int { return s.width }

// Height returns the frame height in pixels.
func (s *Stream) Height() int { return s.height }

// Close terminates the FFmpeg process and releases resources.
func (s *Stream) Close() error {
	// Closing stdout will cause ffmpeg to get a broken-pipe and exit.
	_ = s.stdout.Close()
	return s.cmd.Wait()
}

// buildArgs constructs the ffmpeg CLI arguments for the given config.
func buildArgs(cfg Config) []string {
	size := fmt.Sprintf("%dx%d", cfg.Width, cfg.Height)
	rate := fmt.Sprintf("%d", cfg.FPS)

	switch cfg.Input {
	case "testsrc", "testsrc2", "smptebars", "color":
		// Virtual source filter — no input file needed.
		return []string{
			"-f", "lavfi",
			"-i", fmt.Sprintf("%s=size=%s:rate=%s", cfg.Input, size, rate),
			"-pix_fmt", "rgba",
			"-f", "rawvideo",
			"-v", "error",
			"pipe:1",
		}
	default:
		// File / URL input.
		return []string{
			"-re",          // read at native frame rate
			"-i", cfg.Input,
			"-vf", fmt.Sprintf("scale=%s", size),
			"-r", rate,
			"-pix_fmt", "rgba",
			"-f", "rawvideo",
			"-v", "error",
			"pipe:1",
		}
	}
}
