//go:build !windows

package main

import "errors"

func StartCapture(st *State) error      { return errors.New("live capture only works on Windows") }
func listDevices() error                { return errors.New("only on Windows") }
func openOverlay(url string, st *State) { logf("open %s in a browser", url) }
func fatalBox(msg string)               { logf("%s", msg) }

func applyWindowSettings(Settings)           {}
func windowDrag(st *State, resize bool)      {}
func windowMinimize()                        {}
func windowClose()                           {}
func windowResize(h int)                     {}
func setLockHotspot(x, y, w, h, dpr float64) {}
func runInstaller(path string) error         { return errors.New("only on Windows") }
