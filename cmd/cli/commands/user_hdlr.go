// Package commands provides the set of CLI commands used to communicate with the AIS cluster.
// This file handles commands that create entities in the cluster.
/*
 * Copyright (c) 2019, NVIDIA CORPORATION. All rights reserved.
 */
package commands

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/aistore/api"
	"github.com/NVIDIA/aistore/cmd/cli/templates"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/jsp"
	"github.com/urfave/cli"
)

const (
	credDir  = ".ais"
	credFile = "token"

	authnServerURL         = "AUTHN_URL"
	authnSuperuserName     = "AUTHN_SU_NAME"
	authnSuperuserPassword = "AUTHN_SU_PASS"
	authnUserName          = "AUTHN_USER_NAME"
	authnUserPassword      = "AUTHN_USER_PASS"

	// TODO: fix and move AuthN subcommands to commands/common.go
	subcmdAuthAdd     = "add"
	subcmdAuthShow    = "show"
	subcmdAuthUpdate  = "update"
	subcmdAuthRemove  = commandRemove
	subcmdAuthLogin   = "login"
	subcmdAuthLogout  = "logout"
	subcmdAuthUser    = "user"
	subcmdAuthCluster = "cluster"
)

var (
	authCmds = []cli.Command{
		{
			Name:  commandAuth,
			Usage: "manage AutnN server",
			Subcommands: []cli.Command{
				{
					Name:  subcmdAuthAdd,
					Usage: "add entity from auth",
					Subcommands: []cli.Command{
						{
							Name:      subcmdAuthUser,
							Usage:     "add a new user",
							ArgsUsage: addUserArgument,
							Action:    addUserHandler,
						},
						{
							Name:      subcmdAuthCluster,
							Usage:     "register a new cluster",
							ArgsUsage: addAuthClusterArgument,
							Action:    addAuthClusterHandler,
						},
					},
				},
				{
					Name:  subcmdAuthRemove,
					Usage: "remove entity from auth",
					Subcommands: []cli.Command{
						{
							Name:      subcmdAuthUser,
							Usage:     "remove an existing user",
							ArgsUsage: deleteUserArgument,
							Action:    deleteUserHandler,
						},
						{
							Name:      subcmdAuthCluster,
							Usage:     "unregister a cluster",
							ArgsUsage: deleteAuthClusterArgument,
							Action:    deleteAuthClusterHandler,
						},
					},
				},
				{
					Name: subcmdAuthUpdate,
					Subcommands: []cli.Command{
						{
							Name:      subcmdAuthCluster,
							Usage:     "update registered cluster config",
							ArgsUsage: addAuthClusterArgument,
							Action:    updateAuthClusterHandler,
						},
					},
				},
				{
					Name:  subcmdAuthShow,
					Usage: "show entity in authn",
					Subcommands: []cli.Command{
						{
							Name:      subcmdAuthCluster,
							Usage:     "show registered clusters",
							ArgsUsage: showAuthClusterArgument,
							Action:    showAuthClusterHandler,
						},
					},
				},
				{
					Name:      subcmdAuthLogin,
					Usage:     "log in with existing user credentials",
					ArgsUsage: userLoginArgument,
					Action:    loginUserHandler,
				},
				{
					Name:   subcmdAuthLogout,
					Usage:  "log out",
					Action: logoutUserHandler,
				},
			},
		},
	}

	loggedUserToken api.AuthCreds
)

func readValue(c *cli.Context, prompt string) string {
	fmt.Fprintf(c.App.Writer, prompt+": ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(line, "\n")
}

func cliAuthnURL() string {
	return os.Getenv(authnServerURL)
}

func cliAuthnAdminName(c *cli.Context) string {
	name := os.Getenv(authnSuperuserName)
	if name == "" {
		name = readValue(c, "Superuser login")
	}
	return name
}

func cliAuthnAdminPassword(c *cli.Context) string {
	pass := os.Getenv(authnSuperuserPassword)
	if pass == "" {
		pass = readValue(c, "Superuser password")
	}
	return pass
}

func cliAuthnUserName(c *cli.Context) string {
	name := c.Args().Get(0)
	if name == "" {
		name = os.Getenv(authnUserName)
	}
	if name == "" {
		name = readValue(c, "User login")
	}
	return name
}

func cliAuthnUserPassword(c *cli.Context) string {
	pass := c.Args().Get(1)
	if pass == "" {
		pass = os.Getenv(authnUserPassword)
	}
	if pass == "" {
		pass = readValue(c, "User password")
	}
	return pass
}

func addUserHandler(c *cli.Context) (err error) {
	authnURL := cliAuthnURL()
	if authnURL == "" {
		return fmt.Errorf("AuthN URL is not set") // nolint:golint // name of the service
	}
	baseParams := cliAuthParams(authnURL)
	spec := api.AuthnSpec{
		AdminName:     cliAuthnAdminName(c),
		AdminPassword: cliAuthnAdminPassword(c),
		UserName:      cliAuthnUserName(c),
		UserPassword:  cliAuthnUserPassword(c),
	}
	return api.AddUser(baseParams, spec)
}

func deleteUserHandler(c *cli.Context) (err error) {
	authnURL := cliAuthnURL()
	if authnURL == "" {
		return fmt.Errorf("AuthN URL is not set") // nolint:golint // name of the service
	}
	baseParams := cliAuthParams(authnURL)
	spec := api.AuthnSpec{
		AdminName:     cliAuthnAdminName(c),
		AdminPassword: cliAuthnAdminPassword(c),
		UserName:      cliAuthnUserName(c),
	}
	return api.DeleteUser(baseParams, spec)
}

func loginUserHandler(c *cli.Context) (err error) {
	const tokenSaveFailFmt = "successfully logged in, but failed to save token: %v"
	authnURL := cliAuthnURL()
	if authnURL == "" {
		return fmt.Errorf("AuthN URL is not set") // nolint:golint // name of the service
	}
	baseParams := cliAuthParams(authnURL)
	spec := api.AuthnSpec{
		UserName:     cliAuthnUserName(c),
		UserPassword: cliAuthnUserPassword(c),
	}
	token, err := api.LoginUser(baseParams, spec)
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf(tokenSaveFailFmt, err)
	}

	tokenDir := filepath.Join(home, credDir)
	err = cmn.CreateDir(tokenDir)
	if err != nil {
		return fmt.Errorf(tokenSaveFailFmt, err)
	}
	tokenPath := filepath.Join(tokenDir, credFile)
	err = jsp.Save(tokenPath, token, jsp.Plain())
	if err != nil {
		return fmt.Errorf(tokenSaveFailFmt, err)
	}

	return nil
}

func logoutUserHandler(c *cli.Context) (err error) {
	const logoutFailFmt = "logging out failed: %v"
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf(logoutFailFmt, err)
	}

	tokenPath := filepath.Join(home, credDir, credFile)
	if err = os.Remove(tokenPath); os.IsNotExist(err) {
		return fmt.Errorf(logoutFailFmt, err)
	}
	return nil
}

func addAuthClusterHandler(c *cli.Context) (err error) {
	authnURL := cliAuthnURL()
	if authnURL == "" {
		return fmt.Errorf("AuthN URL is not set") // nolint:golint // name of the service
	}
	baseParams := cliAuthParams(authnURL)
	baseParams.Token = "" // the request requires superuser credentials, not user's ones
	cid := c.Args().Get(0)
	if cid == "" {
		return missingArgumentsError(c, "cluster id")
	}
	urls := c.Args().Get(1)
	if strings.Contains(cid, "=") {
		parts := strings.SplitN(cid, "=", 2)
		cid = parts[0]
		urls = parts[1]
	} else if urls == "" {
		return missingArgumentsError(c, "cluster URL list")
	}
	urlList := strings.Split(urls, ",")

	spec := api.ClusterSpec{
		ClusterID: cid,
		URLs:      urlList,
	}
	return api.RegisterClusterAuthN(baseParams, spec)
}

func updateAuthClusterHandler(c *cli.Context) (err error) {
	authnURL := cliAuthnURL()
	if authnURL == "" {
		return fmt.Errorf("AuthN URL is not set") // nolint:golint // name of the service
	}
	baseParams := cliAuthParams(authnURL)
	baseParams.Token = "" // the request requires superuser credentials, not user's ones
	cid := c.Args().Get(0)
	if cid == "" {
		return missingArgumentsError(c, "cluster id")
	}
	urls := c.Args().Get(1)
	if strings.Contains(cid, "=") {
		parts := strings.SplitN(cid, "=", 2)
		cid = parts[0]
		urls = parts[1]
	} else if urls == "" {
		return missingArgumentsError(c, "cluster URL list")
	}
	urlList := strings.Split(urls, ",")

	spec := api.ClusterSpec{
		ClusterID: cid,
		URLs:      urlList,
	}
	return api.UpdateClusterAuthN(baseParams, spec)
}

func deleteAuthClusterHandler(c *cli.Context) (err error) {
	authnURL := cliAuthnURL()
	if authnURL == "" {
		return fmt.Errorf("AuthN URL is not set") // nolint:golint // name of the service
	}
	baseParams := cliAuthParams(authnURL)
	baseParams.Token = "" // the request requires superuser credentials, not user's ones
	cid := c.Args().Get(0)
	if cid == "" {
		return missingArgumentsError(c, "cluster id")
	}
	spec := api.ClusterSpec{
		ClusterID: cid,
	}
	return api.UnregisterClusterAuthN(baseParams, spec)
}

func showAuthClusterHandler(c *cli.Context) (err error) {
	authnURL := cliAuthnURL()
	if authnURL == "" {
		return fmt.Errorf("AuthN URL is not set") // nolint:golint // name of the service
	}
	baseParams := cliAuthParams(authnURL)
	baseParams.Token = "" // the request requires superuser credentials, not user's ones
	spec := api.ClusterSpec{
		ClusterID: c.Args().Get(0),
	}
	list, err := api.GetClusterAuthN(baseParams, spec)
	if err != nil {
		return err
	}

	return templates.DisplayOutput(list, c.App.Writer, templates.AuthNClusterTmpl)
}
