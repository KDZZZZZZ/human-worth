//go:build integration

package identity

import (
	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/asset"
	assetapp "github.com/KDZZZZZZ/human-worth/backend/internal/asset/application"
	assetpostgres "github.com/KDZZZZZZ/human-worth/backend/internal/asset/repository/postgres"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5/pgxpool"
)

func layeredAssetServer(db *pgxpool.Pool, store asset.Storage, identity identitypb.IdentityServiceClient, content contentpb.ContentServiceClient) *asset.Server {
	useCase := assetapp.BrowserUploadUseCase{Repository: assetpostgres.UploadRepository{DB: db}, Storage: store, ChunkBytes: platform.AssetUploadChunkBytes, MaxAssetBytes: 32 << 20}
	if multipart, ok := store.(assetapp.MultipartStorage); ok {
		useCase.Multipart = multipart
	}
	repository := assetpostgres.UploadRepository{DB: db}
	challenge := assetapp.ChallengeUploadUseCase{Repository: repository, Storage: store, ChunkBytes: platform.AssetUploadChunkBytes, MaxAssetBytes: 32 << 20}
	if multipart, ok := store.(assetapp.ChallengeMultipartStorage); ok {
		challenge.Multipart = multipart
	}
	return &asset.Server{DB: db, Identity: identity, Storage: store, Content: content, BrowserUpload: useCase,
		ChallengeUpload: challenge,
		UploadSessions:  assetapp.SessionUseCase{Repository: repository},
		CompleteUseCase: assetapp.CompleteUploadUseCase{Repository: repository, Storage: store},
		AbortUseCase:    assetapp.AbortUploadUseCase{Repository: repository, Storage: store},
	}
}
