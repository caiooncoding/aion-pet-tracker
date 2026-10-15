//go:build !windows

package main

import "errors"

func StartCapture(st *State) error { return errors.New("live capture only works on Windows") }
func listDevices() error          { return errors.New("only on Windows") }
func openOverlay(url string)       { logf("open %s in a browser", url) }
func fatalBox(msg string)          { logf("%s", msg) }
