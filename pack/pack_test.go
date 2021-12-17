// Copyright (C) 2021 Storj Labs, Inc.
// See LICENSE for copying information.

package pack_test

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"testing"
	"time"

	ds "github.com/ipfs/go-datastore"
	"github.com/jtolio/zipper"
	storjds "github.com/kaloyan-raev/ipfs-go-ds-storj"
	"github.com/kaloyan-raev/ipfs-go-ds-storj/dbx"
	"github.com/kaloyan-raev/ipfs-go-ds-storj/pack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"storj.io/common/memory"
	"storj.io/common/testcontext"
	"storj.io/common/testrand"
	"storj.io/storj/private/testplanet"
	"storj.io/uplink"
)

func TestPack(t *testing.T) {
	testplanet.Run(t, testplanet.Config{
		SatelliteCount:   1,
		StorageNodeCount: 4,
		UplinkCount:      1,
	}, func(t *testing.T, ctx *testcontext.Context, planet *testplanet.Planet) {
		sat := planet.Satellites[0]
		uplnk := planet.Uplinks[0]
		bucket := "testbucket"

		access, err := uplnk.Access[sat.ID()].Serialize()
		require.NoError(t, err)

		project, err := uplnk.GetProject(ctx, sat)
		require.NoError(t, err)

		err = uplnk.CreateBucket(ctx, sat, bucket)
		require.NoError(t, err)

		dbFile, err := ioutil.TempFile(os.TempDir(), "storjds-db-")
		require.NoError(t, err)

		defer func() {
			err := os.Remove(dbFile.Name())
			require.NoError(t, err)
		}()

		storj, err := storjds.NewStorjDatastore(storjds.Config{
			DBPath:       dbFile.Name(),
			Bucket:       bucket,
			AccessGrant:  access,
			PackInterval: 100 * time.Millisecond,
			MinPackSize:  1 * memory.MiB.Int(),
			MaxPackSize:  2 * memory.MiB.Int(),
		})
		require.NoError(t, err)

		defer func() {
			err := storj.Close()
			require.NoError(t, err)
		}()

		var keys []ds.Key
		for i := 0; i < 10; i++ {
			keys = append(keys, ds.NewKey(fmt.Sprintf("block%d", i)))
		}

		var blobs [][]byte
		for i := 0; i < 10; i++ {
			blobs = append(blobs, testrand.Bytes(256*memory.KiB))
		}

		for i, key := range keys {
			err = storj.Put(key, blobs[i])
			require.NoError(t, err)
		}

		err = storj.Sync(ds.Key{})
		require.NoError(t, err)

		time.Sleep(500 * time.Millisecond)

		var objectKey string

		for i, key := range keys {
			block, err := storj.DB().Get_Block_By_Cid(ctx, dbx.Block_Cid(strings.TrimPrefix(key.String(), "/")))
			require.NoError(t, err, i)
			if i < 8 {
				assert.Equal(t, pack.Packed, pack.Status(block.PackStatus), block.Created)
				assert.Nil(t, block.Data, i)
				assert.NotEmpty(t, block.PackObject, i)
				assert.NotZero(t, block.PackOffset, i)
				objectKey = block.PackObject
			} else {
				assert.Equal(t, pack.Unpacked, pack.Status(block.PackStatus), i)
				assert.Equal(t, blobs[i], block.Data, i)
				assert.Empty(t, block.PackObject, i)
				assert.Zero(t, block.PackOffset, i)
			}
		}

		obj, err := project.StatObject(ctx, bucket, objectKey)
		require.NoError(t, err)
		require.Greater(t, obj.System.ContentLength, 2*memory.MiB.Int64())
		require.Equal(t, "application/zip", obj.Custom["content-type"])

		pack, err := zipper.OpenPack(ctx, project, bucket, objectKey)
		require.NoError(t, err)

		for i := 0; i < 8; i++ {
			block, err := pack.Open(ctx, fmt.Sprintf("block%d", i))
			require.NoError(t, err)
			assert.Equal(t, int64(len(blobs[i])), block.Size)

			data, err := ioutil.ReadAll(block)
			require.NoError(t, err)
			assert.Equal(t, blobs[i], data)
		}

		for i := 0; i < 8; i++ {
			block, err := storj.DB().Get_Block_By_Cid(ctx, dbx.Block_Cid(fmt.Sprintf("block%d", i)))
			require.NoError(t, err, i)
			data := readRange(t, ctx, project, bucket, objectKey, block.PackOffset, block.Size)
			assert.Equal(t, blobs[i], data)
		}
	})
}

func readRange(t *testing.T, ctx *testcontext.Context, project *uplink.Project, bucket, key string, offset, length int) []byte {
	download, err := project.DownloadObject(ctx, bucket, key, &uplink.DownloadOptions{
		Offset: int64(offset),
		Length: int64(length),
	})
	require.NoError(t, err)
	defer func() {
		err := download.Close()
		require.NoError(t, err)
	}()

	data, err := ioutil.ReadAll(download)
	require.NoError(t, err)

	return data
}
