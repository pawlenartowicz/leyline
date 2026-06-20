package admin

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// CmdOpts holds the inputs common to every leyline admin verb. Built from
// the inherited cobra flags + the loaded keystore.
type CmdOpts struct {
	DirectKey          string
	DirectServer       string
	Keyname            string
	Keystore           []KeyRow
	JSON               bool
	InsecureSkipVerify bool
	PositionalVault    string
	Stdout             io.Writer
	Stderr             io.Writer
}

// optsFromCmd reads the inherited persistent flags off cmd, loads the
// keystore from --key-file, and returns a CmdOpts ready for ResolveKey.
// `positional` is the vault arg (canonical "<host>/<vaultID>") or "" for
// server-scoped commands.
func optsFromCmd(cmd *cobra.Command, positional string) (CmdOpts, error) {
	key, _ := cmd.Flags().GetString("key")
	srv, _ := cmd.Flags().GetString("server")
	keyname, _ := cmd.Flags().GetString("keyname")
	keyfile, _ := cmd.Flags().GetString("key-file")
	asJSON, _ := cmd.Flags().GetBool("json")
	insecure, _ := cmd.Flags().GetBool("insecure")

	var ks []KeyRow
	if key == "" {
		rows, err := LoadKeystore(keyfile)
		if err != nil {
			return CmdOpts{}, fmt.Errorf("load keystore: %w", err)
		}
		ks = rows
	}
	return CmdOpts{
		DirectKey:          key,
		DirectServer:       srv,
		Keyname:            keyname,
		Keystore:           ks,
		JSON:               asJSON,
		InsecureSkipVerify: insecure,
		PositionalVault:    positional,
		Stdout:             cmd.OutOrStdout(),
		Stderr:             cmd.ErrOrStderr(),
	}, nil
}

// clientFor resolves the right key/server pair and constructs a Client.
func clientFor(opts CmdOpts) (*Client, string, error) {
	r, err := ResolveKey(ResolveInput{
		DirectKey:       opts.DirectKey,
		DirectServer:    opts.DirectServer,
		PositionalVault: opts.PositionalVault,
		Keyname:         opts.Keyname,
		Keystore:        opts.Keystore,
	})
	if err != nil {
		return nil, "", err
	}
	return NewClient(r.ServerURL, r.Key, opts.InsecureSkipVerify), r.ServerURL, nil
}
