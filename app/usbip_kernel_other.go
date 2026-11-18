//go:build !linux

package main

func detectUSBIPKernel() *usbipKernelState { return nil }
