package hid

import "sync"

// Report is one recorded HID report.
type Report struct {
	Keyboard  bool
	Modifiers uint8
	Usages    []uint16
	Buttons   uint32
	DX, DY    int
	VWheel    int
	HWheel    int
}

// Recorder is a Backend that only records reports. It is used by tests and by
// --dry-run, where no driver is installed. OnPointer, when set, sees each
// pointing report (tests use it to move a simulated cursor).
type Recorder struct {
	mu        sync.Mutex
	Reports   []Report
	OnPointer func(dx, dy int)
}

func (r *Recorder) Keyboard(modifiers uint8, usages []uint16) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Reports = append(r.Reports, Report{Keyboard: true, Modifiers: modifiers, Usages: append([]uint16(nil), usages...)})
	return nil
}

func (r *Recorder) Pointer(buttons uint32, dx, dy, vwheel, hwheel int) error {
	r.mu.Lock()
	r.Reports = append(r.Reports, Report{Buttons: buttons, DX: dx, DY: dy, VWheel: vwheel, HWheel: hwheel})
	on := r.OnPointer
	r.mu.Unlock()
	if on != nil {
		on(dx, dy)
	}
	return nil
}

func (r *Recorder) ReleaseAll() error {
	if err := r.Keyboard(0, nil); err != nil {
		return err
	}
	return r.Pointer(0, 0, 0, 0, 0)
}

func (r *Recorder) Close() error { return nil }
