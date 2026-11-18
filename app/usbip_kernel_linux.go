//go:build linux

package main

func detectUSBIPKernel() *usbipKernelState {
	state := detectKernelModules()
	return &state
}
