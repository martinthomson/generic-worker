package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dchest/uniuri"
)

func processCommandOutput(callback func(line string), prog string, options ...string) error {
	out, err := exec.Command(prog, options...).Output()
	if err != nil {
		debug("%v", err)
		return err
	}
	for _, line := range strings.Split(string(out), "\r\n") {
		trimmedLine := strings.Trim(line, "\r\n ")
		callback(trimmedLine)
	}
	return nil
}

func startup() error {
	debug("Detected Windows platform...")
	return taskCleanup()
}

func createNewOSUser() error {
	// username can only be 20 chars, uuids are too long, therefore
	// use prefix (5 chars) plus seconds since epoch (10 chars)
	userName := "Task_" + strconv.Itoa((int)(time.Now().Unix()))
	password := generatePassword()
	User = OSUser{
		HomeDir:  "C:\\Users\\" + userName,
		Name:     userName,
		Password: password,
	}
	debug("Creating Windows User " + User.Name + "...")
	err := os.MkdirAll(User.HomeDir, 0755)
	if err != nil {
		return err
	}
	commandsToRun := [][]string{
		{"icacls", User.HomeDir, "/remove:g", "Users"},
		{"icacls", User.HomeDir, "/remove:g", "Everyone"},
		{"icacls", User.HomeDir, "/inheritance:r"},
		{"net", "user", User.Name, User.Password, "/add", "/expires:never", "/passwordchg:no", "/homedir:" + User.HomeDir, "/y"},
		{"icacls", User.HomeDir, "/grant:r", User.Name + ":(CI)F", "SYSTEM:(CI)F", "Administrators:(CI)F"},
		{"net", "localgroup", "Remote Desktop Users", "/add", User.Name},
		{"C:\\Users\\Administrator\\PSTools\\PsExec.exe",
			"-u", User.Name,
			"-p", User.Password,
			"-w", User.HomeDir,
			"-n", "10",
			"whoami",
		},
	}
	for _, command := range commandsToRun {
		debug("Running command: '" + strings.Join(command, "' '") + "'")
		out, err := exec.Command(command[0], command[1:]...).Output()
		if err != nil {
			debug("%v", err)
			return err
		}
		debug(string(out))
	}
	return os.MkdirAll(filepath.Join(User.HomeDir, "public", "logs"), 0666)
}

// Uses [A-Za-z0-9] characters (default set) to avoid strange escaping problems
// that could potentially affect security. Prefixed with `pWd0_` to ensure
// password contains a special character (_), lowercase and uppercase letters,
// and a number. This is useful if the OS has a strict password policy
// requiring all of these. The total password length is 29 characters (24 of
// which are random). 29 characters should not be too long for the OS. The 24
// random characters of [A-Za-z0-9] provide (26+26+10)^24 possible permutations
// (approx 143 bits of randomness). Randomisation is not seeded, so results
// should not be reproducible.
func generatePassword() string {
	return "pWd0_" + uniuri.NewLen(24)
}

func deleteExistingOSUsers() {
	debug("Looking for existing task users to delete...")
	err := processCommandOutput(deleteOSUserAccount, "wmic", "useraccount", "get", "name")
	if err != nil {
		debug("WARNING: could not list existing Windows user accounts")
		debug("%v", err)
	}
	deleteHomeDirs()
}

func deleteHomeDirs() {
	homeDirsParent, err := os.Open("C:\\Users")
	if err != nil {
		debug("WARNING: Could not open C:\\Users directory to find old home directories to delete")
		debug("%v", err)
		return
	}
	defer homeDirsParent.Close()
	fi, err := homeDirsParent.Readdir(-1)
	if err != nil {
		debug("WARNING: Could not read complete directory listing to find old home directories to delete")
		debug("%v", err)
		// don't return, since we may have partial listings
	}
	for _, file := range fi {
		if file.IsDir() {
			if fileName := file.Name(); strings.HasPrefix(fileName, "Task_") {
				path := "C:\\Users\\" + fileName
				debug("Removing home directory '" + path + "'...")
				err = os.RemoveAll(path)
				if err != nil {
					debug("WARNING: could not delete directory '" + path + "'")
					debug("%v", err)
				}
			}
		}
	}
}

func deleteOSUserAccount(line string) {
	if strings.HasPrefix(line, "Task_") {
		user := line
		debug("Attempting to remove Windows user " + user + "...")
		out, err := exec.Command("net", "user", user, "/delete").Output()
		if err != nil {
			debug("WARNING: Could not remove Windows user account " + user)
			debug("%v", err)
		}
		debug(string(out))
	}
}

func (task *TaskRun) generateCommand(index int) (Command, error) {
	// In order that capturing of log files works, create a custom .bat file
	// for the task which redirects output to a log file...
	commandName := fmt.Sprintf("Command_%06d", index)
	wrapper := filepath.Join(User.HomeDir, commandName+"_wrapper.bat")
	script := filepath.Join(User.HomeDir, commandName+".bat")
	log := filepath.Join(User.HomeDir, "public", "logs", commandName+".log")
	err := ioutil.WriteFile(
		wrapper,
		[]byte(
			":: This script runs command "+strconv.Itoa(index)+" defined in TaskId "+task.TaskId+"..."+"\r\n"+
				"call "+script+" > "+log+" 2>&1"+"\r\n",
		),
		0755,
	)

	if err != nil {
		return Command{}, err
	}

	// Now make the actual task a .bat script
	fileContents := []byte(task.Payload.Command[index] + "\r\n")

	err = ioutil.WriteFile(
		script,
		fileContents,
		0755,
	)

	if err != nil {
		return Command{}, err
	}

	command := []string{
		"C:\\Users\\Administrator\\PSTools\\PsExec.exe",
		"-u", User.Name,
		"-p", User.Password,
		"-w", User.HomeDir,
		"-n", "10",
		wrapper,
	}
	cmd := exec.Command(command[0], command[1:]...)
	debug("Running command: '" + strings.Join(command, "' '") + "'")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// TODO: this should be done in the bat script (not the wrapper)
	task.prepEnvVars(cmd)
	task.Commands[index] = Command{logFile: "public/logs/" + commandName + ".log", osCommand: cmd}
	return task.Commands[index], nil
}

func taskCleanup() error {
	// note if this fails, we carry on without throwing an error
	deleteExistingOSUsers()
	// this needs to succeed, so return an error if it doesn't
	return createNewOSUser()
}
