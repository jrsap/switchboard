// Command switchboard-admin is the admin CLI. At this stage it manages
// users in the global database only (orgs, providers, models and keys come
// later, against the per-org databases).
package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
	_ "modernc.org/sqlite"

	"github.com/jrsap/switchboard/internal/database"
	"github.com/jrsap/switchboard/internal/globaldb"
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)

type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	defaultDB := os.Getenv("SWITCHBOARD_GLOBAL_DB")
	if defaultDB == "" {
		defaultDB = "switchboard-global.db"
	}
	dbPath := flag.String("db", defaultDB, "path to the global database (or SWITCHBOARD_GLOBAL_DB)")
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	var err error
	switch args[0] {
	case "user":
		err = userCommand(*dbPath, args[1:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchboard-admin: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: switchboard-admin [-db PATH] <command> [flags]

Commands:
  user add -username NAME [-password PASS] [-org ORG]...
  user list
  user show -username NAME
  user delete -username NAME
  user set-password -username NAME [-password PASS]
  user set-orgs -username NAME [-org ORG]...

The global database path defaults to SWITCHBOARD_GLOBAL_DB or ./switchboard-global.db.
If -password is omitted on add/set-password, it is prompted for.
`)
}

func userCommand(dbPath string, args []string) error {
	if len(args) == 0 {
		return errors.New("user: missing subcommand (add, list, show, delete, set-password, set-orgs)")
	}
	db, err := openGlobalDB(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	switch args[0] {
	case "add":
		return userAdd(db, args[1:])
	case "list":
		return userList(db)
	case "show":
		return userShow(db, args[1:])
	case "delete":
		return userDelete(db, args[1:])
	case "set-password":
		return userSetPassword(db, args[1:])
	case "set-orgs":
		return userSetOrgs(db, args[1:])
	default:
		return fmt.Errorf("user: unknown subcommand %q", args[0])
	}
}

func openGlobalDB(path string) (*globaldb.DB, error) {
	sqlDB, err := sql.Open("sqlite", database.SQLiteDSN(path))
	if err != nil {
		return nil, err
	}
	return globaldb.Open(sqlDB)
}

func userAdd(db *globaldb.DB, args []string) error {
	fs := flag.NewFlagSet("user add", flag.ExitOnError)
	username := fs.String("username", "", "username (required)")
	password := fs.String("password", "", "password (prompted if omitted)")
	var orgs stringSlice
	fs.Var(&orgs, "org", "org membership (routing cache), repeatable")
	fs.Parse(args)

	if *username == "" || !usernameRe.MatchString(*username) {
		return fmt.Errorf("invalid username %q: 1-64 chars of [a-zA-Z0-9_.-] required", *username)
	}
	pass, err := passwordValue(*password)
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u, err := db.CreateUser(*username, string(hash), orgs)
	if err != nil {
		return err
	}
	printUser(u)
	return nil
}

func userList(db *globaldb.DB) error {
	users, err := db.ListUsers()
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tUSERNAME\tORGS\tCREATED")
	for _, u := range users {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", u.ID, u.Username, strings.Join(u.Orgs, ","), u.CreatedAt.Format("2006-01-02 15:04:05"))
	}
	return w.Flush()
}

func userShow(db *globaldb.DB, args []string) error {
	fs := flag.NewFlagSet("user show", flag.ExitOnError)
	username := fs.String("username", "", "username (required)")
	fs.Parse(args)
	u, err := lookupByUsername(db, *username)
	if err != nil {
		return err
	}
	printUser(u)
	return nil
}

func userDelete(db *globaldb.DB, args []string) error {
	fs := flag.NewFlagSet("user delete", flag.ExitOnError)
	username := fs.String("username", "", "username (required)")
	fs.Parse(args)
	u, err := lookupByUsername(db, *username)
	if err != nil {
		return err
	}
	if err := db.DeleteUser(u.ID); err != nil {
		return err
	}
	fmt.Printf("deleted user %q (id %d)\n", u.Username, u.ID)
	return nil
}

func userSetPassword(db *globaldb.DB, args []string) error {
	fs := flag.NewFlagSet("user set-password", flag.ExitOnError)
	username := fs.String("username", "", "username (required)")
	password := fs.String("password", "", "new password (prompted if omitted)")
	fs.Parse(args)
	u, err := lookupByUsername(db, *username)
	if err != nil {
		return err
	}
	pass, err := passwordValue(*password)
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := db.SetPassword(u.ID, string(hash)); err != nil {
		return err
	}
	fmt.Printf("password updated for %q\n", u.Username)
	return nil
}

func userSetOrgs(db *globaldb.DB, args []string) error {
	fs := flag.NewFlagSet("user set-orgs", flag.ExitOnError)
	username := fs.String("username", "", "username (required)")
	var orgs stringSlice
	fs.Var(&orgs, "org", "org membership (routing cache), repeatable; replaces the current list")
	fs.Parse(args)
	u, err := lookupByUsername(db, *username)
	if err != nil {
		return err
	}
	if err := db.SetOrgs(u.ID, orgs); err != nil {
		return err
	}
	fmt.Printf("orgs updated for %q: %s\n", u.Username, strings.Join(orgs, ","))
	return nil
}

func lookupByUsername(db *globaldb.DB, username string) (*globaldb.User, error) {
	if username == "" {
		return nil, errors.New("-username is required")
	}
	u, err := db.GetUserByUsername(username)
	if errors.Is(err, globaldb.ErrNotFound) {
		return nil, fmt.Errorf("user %q not found", username)
	}
	return u, err
}

func passwordValue(flagVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	fmt.Fprint(os.Stderr, "Password: ")
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if len(raw) == 0 {
		return "", errors.New("password must not be empty")
	}
	fmt.Fprint(os.Stderr, "Confirm password: ")
	confirm, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if string(raw) != string(confirm) {
		return "", errors.New("passwords do not match")
	}
	return string(raw), nil
}

func printUser(u *globaldb.User) {
	fmt.Printf("id:       %d\nusername: %s\norgs:     %s\ncreated:  %s\n",
		u.ID, u.Username, strings.Join(u.Orgs, ","), u.UpdatedAt.Format("2006-01-02 15:04:05"))
}
