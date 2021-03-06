// Package commands provides the set of CLI commands used to communicate with the AIS cluster.
// This file contains util functions and types.
/*
 * Copyright (c) 2019, NVIDIA CORPORATION. All rights reserved.
 */
package commands

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/aistore/api"
	"github.com/NVIDIA/aistore/cluster"
	"github.com/NVIDIA/aistore/cmd/cli/config"
	"github.com/NVIDIA/aistore/cmd/cli/templates"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/containers"
	"github.com/NVIDIA/aistore/stats"
	"github.com/urfave/cli"
	"github.com/vbauerster/mpb/v4"
	"github.com/vbauerster/mpb/v4/decor"
)

const (
	infinity             = -1
	keyAndValueSeparator = "="
	fileStdIO            = "-"

	// Error messages
	dockerErrMsgFmt  = "Failed to discover docker proxy URL: %v.\nUsing default %q.\n"
	invalidDaemonMsg = "%s is not a valid DAEMON_ID"
	invalidCmdMsg    = "invalid command name '%s'"

	// Scheme parsing
	defaultScheme = "https"
	gsScheme      = "gs"
	s3Scheme      = "s3"
	azScheme      = "az"
	aisScheme     = "ais"
	gsHost        = "storage.googleapis.com"
	s3Host        = "s3.amazonaws.com"

	sizeArg  = "SIZE"
	unitsArg = "UNITS"

	incorrectCmdDistance = 3
)

var (
	clusterURL        string
	defaultHTTPClient *http.Client
	authnHTTPClient   *http.Client
	defaultAPIParams  api.BaseParams
	mu                sync.Mutex
)

//
// Error handling
//

type usageError struct {
	context       *cli.Context
	message       string
	bottomMessage string
	helpData      interface{}
	helpTemplate  string
}

type additionalInfoError struct {
	baseErr        error
	additionalInfo string
}

type progressBarArgs struct {
	barType string
	barText string
	total   int64
	options []mpb.BarOption
}

func (e *usageError) Error() string {
	msg := helpMessage(e.helpTemplate, e.helpData)
	if e.bottomMessage != "" {
		msg += fmt.Sprintf("\n%s\n", e.bottomMessage)
	}
	if e.context.Command.Name != "" {
		return fmt.Sprintf("Incorrect usage of \"%s %s\": %s.\n\n%s", e.context.App.Name, e.context.Command.Name, e.message, msg)
	}
	return fmt.Sprintf("Incorrect usage of \"%s\": %s.\n\n%s", e.context.App.Name, e.message, msg)
}

func helpMessage(template string, data interface{}) string {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)

	// Execute the template that generates command usage text
	cli.HelpPrinterCustom(w, template, data, templates.HelpTemplateFuncMap)
	_ = w.Flush()

	return buf.String()
}

func incorrectUsageError(c *cli.Context, err error) error {
	cmn.Assert(err != nil)
	return &usageError{
		context:      c,
		message:      err.Error(),
		helpData:     c.Command,
		helpTemplate: templates.ShortUsageTmpl,
	}
}

func incorrectUsageMsg(c *cli.Context, fmtString string, args ...interface{}) error {
	return &usageError{
		context:      c,
		message:      fmt.Sprintf(fmtString, args...),
		helpData:     c.Command,
		helpTemplate: templates.ShortUsageTmpl,
	}
}

func objectNameArgumentNotSupported(c *cli.Context, objectName string) error {
	return incorrectUsageMsg(c, "object name %q argument not supported", objectName)
}

func missingArgumentsError(c *cli.Context, missingArgs ...string) error {
	cmn.Assert(len(missingArgs) > 0)
	return &usageError{
		context:      c,
		message:      fmt.Sprintf("missing arguments %q", strings.Join(missingArgs, ", ")),
		helpData:     c.Command,
		helpTemplate: cli.CommandHelpTemplate,
	}
}

func commandNotFoundError(c *cli.Context, cmd string) error {
	return &usageError{
		context:       c,
		message:       fmt.Sprintf("unknown command %q", cmd),
		helpData:      c.App,
		helpTemplate:  templates.ShortUsageTmpl,
		bottomMessage: didYouMeanMessage(c, cmd),
	}
}

func didYouMeanMessage(c *cli.Context, cmd string) string {
	closestCommand, distance := findClosestCommand(cmd, c.App.VisibleCommands())
	if distance >= cmn.Max(incorrectCmdDistance, len(cmd)/2) {
		return ""
	}

	sb := &strings.Builder{}
	sb.WriteString(c.App.Name)
	sb.WriteString(" " + closestCommand)
	for _, a := range c.Args()[1:] { // skip first arg - it is the wrong command
		sb.WriteString(" " + a)
	}
	for _, f := range c.FlagNames() {
		sb.WriteString(" --" + f)
	}
	return fmt.Sprintf("Did you mean: %q?", sb.String())
}

func findClosestCommand(cmd string, candidates []cli.Command) (result string, distance int) {
	var (
		minDist     = math.MaxInt64
		closestName string
	)
	for i := 0; i < len(candidates); i++ {
		dist := cmn.DamerauLevenstheinDistance(cmd, candidates[i].Name)
		if dist < minDist {
			minDist = dist
			closestName = candidates[i].Name
		}
	}
	return closestName, minDist
}

func (e *additionalInfoError) Error() string {
	return fmt.Sprintf("%s. %s", e.baseErr.Error(), cmn.StrToSentence(e.additionalInfo))
}

func newAdditionalInfoError(err error, info string) error {
	cmn.Assert(err != nil)
	return &additionalInfoError{
		baseErr:        err,
		additionalInfo: info,
	}
}

//
// Smap
//

// Populates the proxy and target maps
func fillMap() (*cluster.Smap, error) {
	var (
		wg = &sync.WaitGroup{}
	)
	smap, err := api.GetClusterMap(defaultAPIParams)
	if err != nil {
		return nil, err
	}
	// Get the primary proxy's smap
	smapPrimary, err := api.GetNodeClusterMap(defaultAPIParams, smap.ProxySI.ID())
	if err != nil {
		return nil, err
	}

	proxyCount := smapPrimary.CountProxies()
	targetCount := smapPrimary.CountTargets()

	wg.Add(proxyCount + targetCount)
	retrieveStatus(smapPrimary.Pmap, proxy, wg)
	retrieveStatus(smapPrimary.Tmap, target, wg)
	wg.Wait()
	return smapPrimary, nil
}

func retrieveStatus(nodeMap cluster.NodeMap, daeMap map[string]*stats.DaemonStatus, wg *sync.WaitGroup) {
	fill := func(node *cluster.Snode) {
		obj, _ := api.GetDaemonStatus(defaultAPIParams, node)
		mu.Lock()
		daeMap[node.ID()] = obj
		mu.Unlock()
	}

	for _, si := range nodeMap {
		go func(si *cluster.Snode) {
			fill(si)
			wg.Done()
		}(si)
	}
}

//
// Scheme
//

// Replace protocol (gs://, s3://) with proper google cloud / s3 URL
func parseSource(rawURL string) (link string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return
	}

	scheme := u.Scheme
	host := u.Host
	fullPath := u.Path

	// if rawURL is using gs or s3 scheme ({gs/s3}://<bucket>/...) then <bucket> is considered a host by `url.Parse`
	switch u.Scheme {
	case gsScheme:
		scheme = "https"
		host = gsHost
		fullPath = path.Join(u.Host, fullPath)
	case s3Scheme:
		scheme = "http"
		host = s3Host
		fullPath = path.Join(u.Host, fullPath)
	case "":
		scheme = defaultScheme
	case "https", "http":
		break
	case aisScheme:
		scheme = "http"
		if !strings.Contains(host, ":") {
			host += ":8080"
		}
		fullPath = path.Join(cmn.Version, cmn.Objects, fullPath)
	default:
		err = fmt.Errorf("invalid scheme: %s", scheme)
		return
	}

	normalizedURL := url.URL{
		Scheme:   scheme,
		User:     u.User,
		Host:     host,
		Path:     fullPath,
		RawQuery: u.RawQuery,
		Fragment: u.Fragment,
	}
	return url.QueryUnescape(normalizedURL.String())
}

func parseDest(rawURL string) (bucket, pathSuffix string, err error) {
	destScheme, destBucket, destPathSuffix, err := parseURI(rawURL)
	if err != nil {
		return
	}
	if destScheme != aisScheme {
		err = fmt.Errorf("destination must look as %q, for instance: %s://bucket/objName (got %s)",
			aisScheme, aisScheme, destScheme)
		return
	}
	if destBucket == "" {
		err = fmt.Errorf("destination bucket name cannot be omitted")
		return
	}
	destPathSuffix = strings.Trim(destPathSuffix, "/")
	return destBucket, destPathSuffix, nil
}

func parseURI(rawURL string) (scheme, bucket, objName string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return
	}

	scheme = u.Scheme
	bucket = u.Host
	objName = u.Path
	return
}

func getPrefixFromPrimary() (string, error) {
	smap, err := api.GetClusterMap(defaultAPIParams)
	if err != nil {
		return "", err
	}

	cfg, err := api.GetDaemonConfig(defaultAPIParams, smap.ProxySI.ID())
	if err != nil {
		return "", err
	}

	return cfg.Net.HTTP.Proto + "://", nil
}

//
// Flags
//

// If the flag has multiple values (separated by comma), take the first one
func cleanFlag(flag string) string {
	return strings.Split(flag, ",")[0]
}

func flagIsSet(c *cli.Context, flag cli.Flag) bool {
	// If the flag name has multiple values, take the first one
	flagName := cleanFlag(flag.GetName())
	return c.GlobalIsSet(flagName) || c.IsSet(flagName)
}

// Returns the value of a string flag (either parent or local scope)
func parseStrFlag(c *cli.Context, flag cli.StringFlag) string {
	flagName := cleanFlag(flag.GetName())
	if c.GlobalIsSet(flagName) {
		return c.GlobalString(flagName)
	}
	return c.String(flagName)
}

// Returns the value of an int flag (either parent or local scope)
func parseIntFlag(c *cli.Context, flag cli.IntFlag) int {
	flagName := cleanFlag(flag.GetName())
	if c.GlobalIsSet(flagName) {
		return c.GlobalInt(flagName)
	}
	return c.Int(flagName)
}

// Returns the value of an duration flag (either parent or local scope)
func parseDurationFlag(c *cli.Context, flag cli.Flag) time.Duration {
	flagName := cleanFlag(flag.GetName())
	if c.GlobalIsSet(flagName) {
		return c.GlobalDuration(flagName)
	}
	return c.Duration(flagName)
}

func parseByteFlagToInt(c *cli.Context, flag cli.Flag) (int64, error) {
	flagValue := parseStrFlag(c, flag.(cli.StringFlag))
	b, err := cmn.S2B(flagValue)
	if err != nil {
		return 0, fmt.Errorf("%s (%s) is invalid, expected either a number or a number with a size suffix (kb, MB, GiB, ...)", flag.GetName(), flagValue)
	}
	return b, nil
}

// Returns a string containing the value of the `flag` in bytes, used for `offset` and `length` flags
func getByteFlagValue(c *cli.Context, flag cli.Flag) (string, error) {
	if flagIsSet(c, flag) {
		offsetInt, err := parseByteFlagToInt(c, flag)
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(offsetInt, 10), nil
	}

	return "", nil
}

func calcRefreshRate(c *cli.Context) time.Duration {
	const (
		refreshRateMin = 500 * time.Millisecond
	)

	refreshRate := refreshRateDefault
	if flagIsSet(c, refreshFlag) {
		refreshRate = parseDurationFlag(c, refreshFlag)
		if refreshRate < refreshRateMin {
			refreshRate = refreshRateMin
		}
	}
	return refreshRate
}

//
// Long run parameters
//

type longRunParams struct {
	count       int
	refreshRate time.Duration
}

func defaultLongRunParams() *longRunParams {
	return &longRunParams{
		count:       countDefault,
		refreshRate: refreshRateDefault,
	}
}

func (p *longRunParams) isInfiniteRun() bool {
	return p.count == infinity
}

func updateLongRunParams(c *cli.Context) error {
	params := c.App.Metadata[metadata].(*longRunParams)

	if flagIsSet(c, refreshFlag) {
		params.refreshRate = parseDurationFlag(c, refreshFlag)
		// Run forever unless `count` is also specified
		params.count = infinity
	}

	if flagIsSet(c, countFlag) {
		params.count = parseIntFlag(c, countFlag)
		if params.count <= 0 {
			_, _ = fmt.Fprintf(c.App.ErrWriter, "Warning: '%s' set to %d, but expected value >= 1. Assuming '%s' = %d.\n",
				countFlag.Name, params.count, countFlag.Name, countDefault)
			params.count = countDefault
		}
	}

	return nil
}

//
// Utility functions
//

// Users can pass in a delimiter separated list
func makeList(list, delimiter string) []string {
	cleanList := strings.Split(list, delimiter)
	for ii, val := range cleanList {
		cleanList[ii] = strings.TrimSpace(val)
	}
	return cleanList
}

// Converts a list of "key value" and "key=value" into map
func makePairs(args []string) (nvs cmn.SimpleKVs, err error) {
	var (
		i  int
		ll = len(args)
	)
	nvs = cmn.SimpleKVs{}
	for i < ll {
		if args[i] != keyAndValueSeparator && strings.Contains(args[i], keyAndValueSeparator) {
			pairs := makeList(args[i], keyAndValueSeparator)
			if pairs[1] == "" {
				return nil, fmt.Errorf("no value for '%s'", pairs[0])
			}
			nvs[pairs[0]] = pairs[1]
			i++
		} else if i < ll-2 && args[i+1] == keyAndValueSeparator {
			nvs[args[i]] = args[i+2]
			i += 3
		} else {
			// last name without a value
			if i == ll-1 {
				return nil, fmt.Errorf("no value for '%s'", args[i])
			}
			nvs[args[i]] = args[i+1]
			i += 2
		}
	}
	return
}

func chooseTmpl(tmplShort, tmplLong string, useShort bool) string {
	if useShort {
		return tmplShort
	}
	return tmplLong
}

// Replace provider aliases with real provider names
func parseBckProvider(provider string) string {
	if provider == gsScheme {
		return cmn.ProviderGoogle
	}
	if provider == s3Scheme {
		return cmn.ProviderAmazon
	}
	if provider == azScheme {
		return cmn.ProviderAzure
	}
	return provider
}

// Parses "provider://@uuid#namespace/bucketName/objectName"
func parseBckObjectURI(objName string, query ...bool) (bck cmn.Bck, object string, err error) {
	const (
		bucketSepa = "/"
	)

	parts := strings.SplitN(objName, cmn.BckProviderSeparator, 2)
	if len(parts) > 1 {
		bck.Provider = parseBckProvider(parts[0])
		objName = parts[1]
	}
	bck.Provider = parseBckProvider(bck.Provider)
	if bck.Provider != "" && !cmn.IsValidProvider(bck.Provider) && bck.Provider != cmn.AnyCloud {
		return bck, "", fmt.Errorf("invalid bucket provider %q", bck.Provider)
	}

	parts = strings.SplitN(objName, bucketSepa, 2)
	if len(parts[0]) > 0 && (parts[0][0] == cmn.NsUUIDPrefix || parts[0][0] == cmn.NsNamePrefix) {
		bck.Ns = cmn.ParseNsUname(parts[0])
		if err := bck.Ns.Validate(); err != nil {
			return bck, "", err
		}
		if len(parts) == 1 {
			isQuery := len(query) > 0 && query[0]
			if parts[0] == string(cmn.NsUUIDPrefix) && isQuery {
				// Case: "[provider://]@" (only valid if uri is query)
				// We need to list buckets from all possible remote clusters
				bck.Ns = cmn.NsAnyRemote
				return bck, "", nil
			}

			// Case: "[provider://]@uuid#ns"
			return bck, "", nil
		}

		// Case: "[provider://]@uuid#ns/bucket"
		parts = strings.SplitN(parts[1], bucketSepa, 2)
	}

	bck.Name = parts[0]
	if bck.Name != "" {
		if err := cmn.ValidateBckName(bck.Name); err != nil {
			return bck, "", err
		}
	}
	if len(parts) > 1 {
		object = parts[1]
	}
	return
}

func parseAliasURL(c *cli.Context) (alias, remAisURL string, err error) {
	var parts []string
	if c.NArg() == 0 {
		err = missingArgumentsError(c, aliasURLPairArgument)
		return
	}
	if c.NArg() > 1 {
		alias, remAisURL = c.Args().Get(0), c.Args().Get(1)
		goto ret
	}
	parts = strings.SplitN(c.Args().First(), keyAndValueSeparator, 2)
	if len(parts) < 2 {
		err = missingArgumentsError(c, aliasURLPairArgument)
		return
	}
	alias, remAisURL = parts[0], parts[1]
ret:
	_, err = url.ParseRequestURI(remAisURL)
	return
}

// Parses [XACTION_ID|XACTION_NAME] [BUCKET_NAME]
func parseXactionFromArgs(c *cli.Context) (xactID, xactKind string, bck cmn.Bck, err error) {
	xactKind = c.Args().Get(0)
	bckName := c.Args().Get(1)
	if !cmn.IsValidXaction(xactKind) {
		xactID = xactKind
		xactKind = ""
	} else {
		switch cmn.XactsMeta[xactKind].Type {
		case cmn.XactTypeGlobal:
			if c.NArg() > 1 {
				fmt.Fprintf(c.App.ErrWriter, "Warning: %q is a global xaction, ignoring bucket name\n", xactKind)
			}
		case cmn.XactTypeBck:
			var objName string
			bck, objName, err = parseBckObjectURI(bckName)
			if err != nil {
				return "", "", bck, err
			}
			if objName != "" {
				return "", "", bck, objectNameArgumentNotSupported(c, objName)
			}
			if bck, err = validateBucket(c, bck, "", true); err != nil {
				return "", "", bck, err
			}
		case cmn.XactTypeTask:
			// TODO: we probably should not ignore bucket...
			if c.NArg() > 1 {
				fmt.Fprintf(c.App.ErrWriter, "Warning: %q is a task xaction, ignoring bucket name\n", xactKind)
			}
		}
	}
	return
}

func validateLocalBuckets(buckets []cmn.Bck, operation string) error {
	for _, bck := range buckets {
		if bck.IsCloud(cmn.AnyCloud) {
			return fmt.Errorf("%s cloud buckets (%s) is not supported", operation, bck)
		}
		bck.Provider = cmn.ProviderAIS
	}
	return nil
}

func bucketsFromArgsOrEnv(c *cli.Context) ([]cmn.Bck, error) {
	bucketNames := c.Args()
	bcks := make([]cmn.Bck, 0, len(bucketNames))

	for _, bucket := range bucketNames {
		bck, objName, err := parseBckObjectURI(bucket)
		if err != nil {
			return nil, err
		}
		if objName != "" {
			return nil, objectNameArgumentNotSupported(c, objName)
		}
		if bucket != "" {
			bcks = append(bcks, bck)
		}
	}

	if len(bcks) != 0 {
		return bcks, nil
	}

	return nil, missingArgumentsError(c, "bucket name")
}

func cliAPIParams(proxyURL string) api.BaseParams {
	return api.BaseParams{
		Client: defaultHTTPClient,
		URL:    proxyURL,
		Token:  loggedUserToken.Token,
	}
}

func cliAuthParams(authnURL string) api.BaseParams {
	return api.BaseParams{
		Client: authnHTTPClient,
		URL:    authnURL,
	}
}

func canReachBucket(bck cmn.Bck) error {
	if _, err := api.HeadBucket(defaultAPIParams, bck); err != nil {
		if httpErr, ok := err.(*cmn.HTTPError); ok {
			if httpErr.Status == http.StatusNotFound {
				return fmt.Errorf("bucket %q does not exist", bck)
			}
		}
		return fmt.Errorf("failed to HEAD bucket %q: %v", bck, err)
	}
	return nil
}

//
// AIS cluster discovery
//

// determineClusterURL resolving order
// 1. cfg.Cluster.URL; if empty:
// 2. Proxy docker container IP address; if not successful:
// 3. Docker default; if not present:
// 4. Default as cfg.Cluster.DefaultAISHost
func determineClusterURL(cfg *config.Config) string {
	if envURL := os.Getenv(cmn.EnvVars.Endpoint); envURL != "" {
		return envURL
	}
	if cfg.Cluster.URL != "" {
		return cfg.Cluster.URL
	}

	if containers.DockerRunning() {
		clustersIDs, err := containers.ClusterIDs()
		if err != nil {
			fmt.Fprintf(os.Stderr, dockerErrMsgFmt, err, cfg.Cluster.DefaultDockerHost)
			return cfg.Cluster.DefaultDockerHost
		}

		cmn.AssertMsg(len(clustersIDs) > 0, "There should be at least one cluster running, when docker running detected.")

		proxyGateway, err := containers.ClusterProxyURL(clustersIDs[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, dockerErrMsgFmt, err, cfg.Cluster.DefaultDockerHost)
			return cfg.Cluster.DefaultDockerHost
		}

		if len(clustersIDs) > 1 {
			fmt.Fprintf(os.Stderr, "Multiple docker clusters running. Connected to %d via %s.\n", clustersIDs[0], proxyGateway)
		}

		return "http://" + proxyGateway + ":8080"
	}

	fmt.Fprintf(os.Stderr, "Warning! AIStore URL not configured, using default: %s\n", cfg.Cluster.DefaultAISHost)
	return cfg.Cluster.DefaultAISHost
}

func printDryRunHeader(c *cli.Context) {
	if flagIsSet(c, dryRunFlag) {
		fmt.Fprintln(c.App.Writer, dryRunHeader+" "+dryRunExplanation)
	}
}

// Prints multiple lines of fmtStr to writer w.
// For line number i, fmtStr is formatted with values of args at index i
// if maxLines >= 0 prints at most maxLines, otherwise prints everything until
// it reaches the end of one of args
func limitedLineWriter(w io.Writer, maxLines int, fmtStr string, args ...[]string) {
	objs := make([]interface{}, 0, len(args))
	if fmtStr == "" || fmtStr[len(fmtStr)-1] != '\n' {
		fmtStr += "\n"
	}

	if maxLines < 0 {
		maxLines = math.MaxInt64
	}
	minLen := math.MaxInt64
	for _, a := range args {
		minLen = cmn.Min(minLen, len(a))
	}

	i := 0
	for {
		for _, a := range args {
			objs = append(objs, a[i])
		}
		fmt.Fprintf(w, fmtStr, objs...)
		i++

		for _, a := range args {
			if len(a) <= i {
				return
			}
		}
		if i >= maxLines {
			fmt.Fprintf(w, "(and %d more)\n", minLen-i)
			return
		}
		objs = objs[:0]
	}
}

func simpleProgressBar(args ...progressBarArgs) (*mpb.Progress, []*mpb.Bar) {
	var (
		progress = mpb.New(mpb.WithWidth(progressBarWidth))
		bars     = make([]*mpb.Bar, 0, len(args))
	)

	for _, a := range args {
		var argDecorators []decor.Decorator
		switch a.barType {
		case unitsArg:
			argDecorators = []decor.Decorator{decor.Name(a.barText, decor.WC{W: len(a.barText) + 1, C: decor.DidentRight}), decor.CountersNoUnit("%d/%d", decor.WCSyncWidth)}
		case sizeArg:
			argDecorators = []decor.Decorator{decor.Name(a.barText, decor.WC{W: len(a.barText) + 1, C: decor.DidentRight}), decor.CountersKibiByte("% .2f / % .2f", decor.WCSyncWidth)}
		default:
			cmn.AssertMsg(false, a.barType+" argument is invalid")
		}

		options := append(
			a.options,
			mpb.PrependDecorators(argDecorators...),
			mpb.AppendDecorators(decor.Percentage(decor.WCSyncWidth)))

		bars = append(bars, progress.AddBar(
			a.total,
			options...))
	}

	return progress, bars
}
