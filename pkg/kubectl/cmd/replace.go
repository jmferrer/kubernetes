/*
Copyright 2014 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/golang/glog"
	"k8s.io/kubernetes/pkg/kubectl"
	cmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
	"k8s.io/kubernetes/pkg/kubectl/resource"
)

const (
	replace_long = `Replace a resource by filename or stdin.

JSON and YAML formats are accepted.

Please refer to the models in https://htmlpreview.github.io/?https://github.com/GoogleCloudPlatform/kubernetes/HEAD/docs/api-reference/definitions.html to find if a field is mutable.`
	replace_example = `// Replace a pod using the data in pod.json.
$ kubectl replace -f ./pod.json

// Replace a pod based on the JSON passed into stdin.
$ cat pod.json | kubectl replace -f -

// Force replace, delete and then re-create the resource
kubectl replace --force -f ./pod.json`
)

func NewCmdReplace(f *cmdutil.Factory, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use: "replace -f FILENAME",
		// update is deprecated.
		Aliases: []string{"update"},
		Short:   "Replace a resource by filename or stdin.",
		Long:    replace_long,
		Example: replace_example,
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(cmdutil.ValidateOutputArgs(cmd))
			err := RunReplace(f, out, cmd, args)
			cmdutil.CheckErr(err)
		},
	}
	usage := "Filename, directory, or URL to file to use to replace the resource."
	kubectl.AddJsonFilenameFlag(cmd, usage)
	cmd.MarkFlagRequired("filename")
	cmd.Flags().Bool("force", false, "Delete and re-create the specified resource")
	cmd.Flags().Bool("cascade", false, "Only relevant during a force replace. If true, cascade the deletion of the resources managed by this resource (e.g. Pods created by a ReplicationController).  Default true.")
	cmd.Flags().Int("grace-period", -1, "Only relevant during a force replace. Period of time in seconds given to the old resource to terminate gracefully. Ignored if negative.")
	cmd.Flags().Duration("timeout", 0, "Only relevant during a force replace. The length of time to wait before giving up on a delete of the old resource, zero means determine a timeout from the size of the object")
	cmdutil.AddOutputFlagsForMutation(cmd)
	return cmd
}

func RunReplace(f *cmdutil.Factory, out io.Writer, cmd *cobra.Command, args []string) error {
	if len(os.Args) > 1 && os.Args[1] == "update" {
		printDeprecationWarning("replace", "update")
	}
	schema, err := f.Validator()
	if err != nil {
		return err
	}

	cmdNamespace, enforceNamespace, err := f.DefaultNamespace()
	if err != nil {
		return err
	}

	force := cmdutil.GetFlagBool(cmd, "force")
	filenames := cmdutil.GetFlagStringSlice(cmd, "filename")
	if len(filenames) == 0 {
		return cmdutil.UsageError(cmd, "Must specify --filename to replace")
	}

	shortOutput := cmdutil.GetFlagString(cmd, "output") == "name"
	if force {
		return forceReplace(f, out, cmd, args, filenames, shortOutput)
	}

	mapper, typer := f.Object()
	r := resource.NewBuilder(mapper, typer, f.ClientMapperForCommand()).
		Schema(schema).
		ContinueOnError().
		NamespaceParam(cmdNamespace).DefaultNamespace().
		FilenameParam(enforceNamespace, filenames...).
		Flatten().
		Do()
	err = r.Err()
	if err != nil {
		return err
	}

	return r.Visit(func(info *resource.Info) error {
		data, err := info.Mapping.Codec.Encode(info.Object)
		if err != nil {
			return cmdutil.AddSourceToErr("replacing", info.Source, err)
		}
		obj, err := resource.NewHelper(info.Client, info.Mapping).Replace(info.Namespace, info.Name, true, data)
		if err != nil {
			return cmdutil.AddSourceToErr("replacing", info.Source, err)
		}
		info.Refresh(obj, true)
		printObjectSpecificMessage(obj, out)
		cmdutil.PrintSuccess(mapper, shortOutput, out, info.Mapping.Resource, info.Name, "replaced")
		return nil
	})
}

func forceReplace(f *cmdutil.Factory, out io.Writer, cmd *cobra.Command, args []string, filenames []string, shortOutput bool) error {
	schema, err := f.Validator()
	if err != nil {
		return err
	}

	cmdNamespace, enforceNamespace, err := f.DefaultNamespace()
	if err != nil {
		return err
	}

	for i, filename := range filenames {
		if filename == "-" {
			tempDir, err := ioutil.TempDir("", "kubectl_replace_")
			if err != nil {
				return err
			}
			defer os.RemoveAll(tempDir)
			tempFilename := filepath.Join(tempDir, "resource.stdin")
			err = cmdutil.DumpReaderToFile(os.Stdin, tempFilename)
			if err != nil {
				return err
			}
			filenames[i] = tempFilename
		}
	}

	mapper, typer := f.Object()
	r := resource.NewBuilder(mapper, typer, f.ClientMapperForCommand()).
		ContinueOnError().
		NamespaceParam(cmdNamespace).DefaultNamespace().
		FilenameParam(enforceNamespace, filenames...).
		ResourceTypeOrNameArgs(false, args...).RequireObject(false).
		Flatten().
		Do()
	err = r.Err()
	if err != nil {
		return err
	}
	//Replace will create a resource if it doesn't exist already, so ignore not found error
	ignoreNotFound := true
	// By default use a reaper to delete all related resources.
	if cmdutil.GetFlagBool(cmd, "cascade") {
		glog.Warningf("\"cascade\" is set, kubectl will delete and re-create all resources managed by this resource (e.g. Pods created by a ReplicationController). Consider using \"kubectl rolling-update\" if you want to update a ReplicationController together with its Pods.")
		err = ReapResult(r, f, out, cmdutil.GetFlagBool(cmd, "cascade"), ignoreNotFound, cmdutil.GetFlagDuration(cmd, "timeout"), cmdutil.GetFlagInt(cmd, "grace-period"), shortOutput, mapper)
	} else {
		err = DeleteResult(r, out, ignoreNotFound, shortOutput, mapper)
	}
	if err != nil {
		return err
	}

	r = resource.NewBuilder(mapper, typer, f.ClientMapperForCommand()).
		Schema(schema).
		ContinueOnError().
		NamespaceParam(cmdNamespace).DefaultNamespace().
		FilenameParam(enforceNamespace, filenames...).
		Flatten().
		Do()
	err = r.Err()
	if err != nil {
		return err
	}

	count := 0
	err = r.Visit(func(info *resource.Info) error {
		data, err := info.Mapping.Codec.Encode(info.Object)
		if err != nil {
			return err
		}
		obj, err := resource.NewHelper(info.Client, info.Mapping).Create(info.Namespace, true, data)
		if err != nil {
			return err
		}
		count++
		info.Refresh(obj, true)
		printObjectSpecificMessage(obj, out)
		cmdutil.PrintSuccess(mapper, shortOutput, out, info.Mapping.Resource, info.Name, "replaced")
		return nil
	})
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("no objects passed to replace")
	}
	return nil
}
