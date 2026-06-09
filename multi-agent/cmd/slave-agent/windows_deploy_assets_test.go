package main

import (
	"os"
	"strings"
	"testing"
)

func TestWindowsSlaveInstallerDefaultsToNativeSkills(t *testing.T) {
	body, err := os.ReadFile("../../deploy/windows/slave/install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `$skills = @("powershell", "file", "permissions")`) {
		t.Fatalf("windows installer must default to native skills only")
	}
	if !strings.Contains(text, `$agentCommand = Get-Command $Agent -ErrorAction SilentlyContinue`) {
		t.Fatalf("windows installer must detect the selected chat backend before advertising chat")
	}
	if strings.Contains(text, `$skills = @("chat", "chat_resume", "powershell", "file", "permissions")`) {
		t.Fatalf("windows installer must not advertise chat/chat_resume by default")
	}
}

func TestWindowsSlaveInstallerDoesNotRegisterBrokenService(t *testing.T) {
	body, err := os.ReadFile("../../deploy/windows/slave/install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "New-Service") {
		t.Fatalf("windows installer must not register a plain PowerShell process as a Windows service")
	}
	if !strings.Contains(text, "InstallService is not supported") {
		t.Fatalf("windows installer should fail clearly when -InstallService is requested")
	}
}
