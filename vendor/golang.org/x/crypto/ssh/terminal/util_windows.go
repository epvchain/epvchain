
// +build windows

package terminal

import (
	"golang.org/x/sys/windows"
)

type State struct {
	mode uint32
}

func IsTerminal(fd int) bool {
	var st uint32
	err := windows.GetConsoleMode(windows.Handle(fd), &st)
	return err == nil
}

func MakeRaw(fd int) (*State, error) {
	var st uint32
	if err := windows.GetConsoleMode(windows.Handle(fd), &st); err != nil {
		return nil, err
	}
	raw := st &^ (windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_LINE_INPUT | windows.ENABLE_PROCESSED_OUTPUT)
	if err := windows.SetConsoleMode(windows.Handle(fd), raw); err != nil {
		return nil, err
	}
	return &State{st}, nil
}

func GetState(fd int) (*State, error) {
	var st uint32
	if err := windows.GetConsoleMode(windows.Handle(fd), &st); err != nil {
		return nil, err
	}
	return &State{st}, nil
}

func Restore(fd int, state *State) error {
	return windows.SetConsoleMode(windows.Handle(fd), state.mode)
}

func GetSize(fd int) (width, height int, err error) {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(fd), &info); err != nil {
		return 0, 0, err
	}
	return int(info.Size.X), int(info.Size.Y), nil
}

type passwordReader int

func (r passwordReader) Read(buf []byte) (int, error) {
	return windows.Read(windows.Handle(r), buf)
}

func ReadPassword(fd int) ([]byte, error) {
	var st uint32
	if err := windows.GetConsoleMode(windows.Handle(fd), &st); err != nil {
		return nil, err
	}
	old := st

	st &^= (windows.ENABLE_ECHO_INPUT)
	st |= (windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_LINE_INPUT | windows.ENABLE_PROCESSED_OUTPUT)
	if err := windows.SetConsoleMode(windows.Handle(fd), st); err != nil {
		return nil, err
	}

	defer func() {
		windows.SetConsoleMode(windows.Handle(fd), old)
	}()

	return readPasswordLine(passwordReader(fd))
}
