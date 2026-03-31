// Package display provides an SDL2-based video window that renders raw RGBA
// frames and processes user input events (e.g. window close).
//
// The window supports dynamic resolution changes so that switching between
// simulcast layers with different dimensions works seamlessly.
package display

import (
	"fmt"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// Window wraps an SDL2 window + renderer + streaming texture.
type Window struct {
	window   *sdl.Window
	renderer *sdl.Renderer
	texture  *sdl.Texture
	width    int32
	height   int32
}

// New creates a new SDL2 window.  The initial size is used for the first
// texture; call SetResolution to adapt when the active layer changes.
func New(title string, width, height int) (*Window, error) {
	if err := sdl.Init(sdl.INIT_VIDEO | sdl.INIT_EVENTS); err != nil {
		return nil, fmt.Errorf("display: sdl init: %w", err)
	}

	w, r, err := sdl.CreateWindowAndRenderer(
		int32(width), int32(height),
		sdl.WINDOW_SHOWN|sdl.WINDOW_RESIZABLE,
	)
	if err != nil {
		sdl.Quit()
		return nil, fmt.Errorf("display: create window: %w", err)
	}
	w.SetTitle(title)

	tex, err := createTexture(r, int32(width), int32(height))
	if err != nil {
		r.Destroy()
		w.Destroy()
		sdl.Quit()
		return nil, err
	}

	return &Window{
		window:   w,
		renderer: r,
		texture:  tex,
		width:    int32(width),
		height:   int32(height),
	}, nil
}

// SetResolution recreates the internal texture to match a new frame size.
// This is called when the simulcast router switches between layers with
// different resolutions (e.g. SD -> HD).  It also resizes the SDL window.
// It is a no-op if the dimensions have not changed.
func (d *Window) SetResolution(width, height int) error {
	w, h := int32(width), int32(height)
	if w == d.width && h == d.height {
		return nil
	}

	// Destroy old texture and create a new one at the target size.
	if d.texture != nil {
		d.texture.Destroy()
	}

	tex, err := createTexture(d.renderer, w, h)
	if err != nil {
		return err
	}

	d.texture = tex
	d.width = w
	d.height = h

	// Resize the window to match the new resolution.
	d.window.SetSize(w, h)
	return nil
}

// UpdateFrame uploads a raw RGBA frame to the texture and presents it.
// The frame buffer must contain at least width*height*4 bytes.
func (d *Window) UpdateFrame(frame []byte) error {
	pitch := int(d.width) * 4 // bytes per row

	if err := d.texture.Update(nil, unsafe.Pointer(&frame[0]), pitch); err != nil {
		return fmt.Errorf("display: texture update: %w", err)
	}

	d.renderer.Clear()
	d.renderer.Copy(d.texture, nil, nil)
	d.renderer.Present()
	return nil
}

// PollQuit processes pending SDL events and returns true when the user
// requests to close the window (click the X button or press Escape / Q).
func (d *Window) PollQuit() bool {
	for event := sdl.PollEvent(); event != nil; event = sdl.PollEvent() {
		switch e := event.(type) {
		case *sdl.QuitEvent:
			return true
		case *sdl.KeyboardEvent:
			if e.Type == sdl.KEYDOWN {
				switch e.Keysym.Sym {
				case sdl.K_ESCAPE, sdl.K_q:
					return true
				}
			}
		}
	}
	return false
}

// Destroy releases all SDL resources.
func (d *Window) Destroy() {
	if d.texture != nil {
		d.texture.Destroy()
	}
	if d.renderer != nil {
		d.renderer.Destroy()
	}
	if d.window != nil {
		d.window.Destroy()
	}
	sdl.Quit()
}

// createTexture builds a streaming ABGR8888 texture (matching Go's RGBA
// byte order on little-endian machines).
func createTexture(r *sdl.Renderer, w, h int32) (*sdl.Texture, error) {
	tex, err := r.CreateTexture(
		sdl.PIXELFORMAT_ABGR8888,
		sdl.TEXTUREACCESS_STREAMING,
		w, h,
	)
	if err != nil {
		return nil, fmt.Errorf("display: create texture %dx%d: %w", w, h, err)
	}
	return tex, nil
}
