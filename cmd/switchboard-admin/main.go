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
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
	_ "modernc.org/sqlite"

	"github.com/jrsap/switchboard/internal/database"
	"github.com/jrsap/switchboard/internal/globaldb"
	"github.com/jrsap/switchboard/internal/orgs"
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
	defaultConfig := os.Getenv("SWITCHBOARD_CONFIG")
	if defaultConfig == "" {
		defaultConfig = "config.json"
	}
	defaultDataDir := os.Getenv("SWITCHBOARD_DATA_DIR")
	if defaultDataDir == "" {
		defaultDataDir = "data"
	}
	dbPath := flag.String("db", defaultDB, "path to the global database (or SWITCHBOARD_GLOBAL_DB)")
	configPath := flag.String("config", defaultConfig, "path to the router config (or SWITCHBOARD_CONFIG)")
	dataDir := flag.String("data-dir", defaultDataDir, "base directory for per-org storage (or SWITCHBOARD_DATA_DIR)")
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
	case "org":
		err = orgCommand(*dbPath, *configPath, *dataDir, args[1:])
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
	fmt.Fprintf(os.Stderr, `Usage: switchboard-admin [-db PATH] [-config PATH] [-data-dir DIR] <command> [flags]

Commands:
  user add -username NAME [-password PASS] [-org ORG]...
  user list
  user show -username NAME
  user delete -username NAME
  user set-password -username NAME [-password PASS]
  user set-orgs -username NAME [-org ORG]...
  org add -id ID -name NAME -admin-username NAME [-admin-password PASS] [-child-addr host:port]
  org list

The global database path defaults to SWITCHBOARD_GLOBAL_DB or ./switchboard-global.db.
The router config defaults to SWITCHBOARD_CONFIG or ./config.json; per-org storage lives
under SWITCHBOARD_DATA_DIR or ./data. If -password/-admin-password is omitted, it is
prompted for.
`)
}

func orgCommand(dbPath, configPath, dataDir string, args []string) error {
	if len(args) == 0 {
		return errors.New("org: missing subcommand (add, list)")
	}
	switch args[0] {
	case "add":
		return orgAdd(dbPath, configPath, dataDir, args[1:])
	case "list":
		return orgList(configPath, args[1:])
	default:
		return fmt.Errorf("org: unknown subcommand %q", args[0])
	}
}

func orgAdd(dbPath, configPath, dataDir string, args []string) error {
	fs := flag.NewFlagSet("org add", flag.ExitOnError)
	id := fs.String("id", "", "org id (lowercase [a-z0-9-], also the API key prefix) (required)")
	name := fs.String("name", "", "org name (required)")
	adminUsername := fs.String("admin-username", "", "username of the admin created with the org (required)")
	adminPassword := fs.String("admin-password", "", "password for the admin user if it must be created (prompted if omitted)")
	childAddr := fs.String("child-addr", "", "loopback host:port for the org child (allocated if omitted)")
	fs.Parse(args)

	if *id == "" || *name == "" || *adminUsername == "" || !usernameRe.MatchString(*adminUsername) {
		return errors.New("org add: -id, -name and a valid -admin-username are required")
	}

	rootKey, err := orgs.RootKeyFromConfig(configPath)
	if err != nil {
		return err
	}
	gdb, err := openGlobalDB(dbPath)
	if err != nil {
		return err
	}
	defer gdb.Close()

	// Only prompt for a password if the global user does not exist yet.
	var hash string
	needPassword := false
	if _, err := gdb.GetUserByUsername(*adminUsername); errors.Is(err, globaldb.ErrNotFound) {
		needPassword = true
	} else if err != nil {
		return err
	}
	if needPassword {
		pass, err := passwordValue(*adminPassword)
		if err != nil {
			return err
		}
		h, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		hash = string(h)
	}

	org, err := orgs.Create(orgs.CreateOptions{
		ConfigPath:        configPath,
		DataDir:           dataDir,
		ID:                *id,
		Name:              *name,
		ChildAddr:         *childAddr,
		AdminUsername:     *adminUsername,
		AdminPasswordHash: hash,
		RootKey:           rootKey,
		GlobalDB:          gdb,
		OpenSQL:           openOrgSQL,
	})
	if err != nil {
		return err
	}
	abs, _ := filepath.Abs(org.Storage.DB)
	fmt.Printf("created org %q (id %s)\n  name:        %s\n  child_addr:  %s\n  org db:      %s\n  archive:     %s\n  admin user:  %s\nrestart the router to spawn its child process\n",
		org.Name, org.ID, org.Name, org.ChildAddr, abs, org.Storage.ArchiveRoot, *adminUsername)
	return nil
}

func orgList(configPath string, args []string) error {
	fs := flag.NewFlagSet("org list", flag.ExitOnError)
	fs.Parse(args)
	orgsList, err := orgs.List(configPath)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tCHILD_ADDR\tORG DB")
	for _, o := range orgsList {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", o.ID, o.Name, o.ChildAddr, o.Storage.DB)
	}
	return w.Flush()
}

func openOrgSQL(path string) (*sql.DB, error) {
	return sql.Open("sqlite", database.SQLiteDSN(path))
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
