package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/containers/azcontainerregistry"
	"github.com/distribution/reference"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
)

type Registry struct {
	Credential azcore.TokenCredential
	Transport  policy.Transporter
}

type registryTransport struct {
	host string
	next policy.Transporter
}

func (t registryTransport) Do(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.Host != t.host || request.URL.User != nil ||
		request.URL.Port() != "" || request.URL.Fragment != "" {
		return nil, errors.New("registry request escaped verified ACR destination")
	}
	if t.next != nil {
		return t.next.Do(request)
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("registry redirects are prohibited")
	}}
	return client.Do(request)
}

// Resolve verifies manifest bytes by tag, then fetches the resulting immutable digest.
func (r *Registry) Resolve(ctx context.Context, image string) (string, error) {
	return r.resolve(ctx, image, "")
}

func (r *Registry) ResolveBuilt(ctx context.Context, image, imageHash string) (string, error) {
	if !strings.HasPrefix(imageHash, "sha256:") {
		imageHash = "sha256:" + imageHash
	}
	if err := config.ValidateImage("example.azurecr.io/config@" + imageHash); err != nil {
		return "", errors.New("native package artifact lacks a full SHA-256 image/config identity")
	}
	return r.resolve(ctx, image, imageHash)
}

func (r *Registry) resolve(ctx context.Context, image, expectedConfig string) (string, error) {
	parsed, err := reference.ParseNamed(image)
	if err != nil {
		return "", errors.New("published image artifact is not a fully qualified OCI reference")
	}
	host := reference.Domain(parsed)
	if !strings.HasSuffix(host, ".azurecr.io") || strings.Contains(host, ":") {
		return "", errors.New("only verified public-cloud ACR destinations are supported")
	}
	repository := reference.Path(parsed)
	selector := ""
	if digest, ok := parsed.(reference.Digested); ok {
		selector = digest.Digest().String()
	} else if tagged, ok := parsed.(reference.Tagged); ok {
		selector = tagged.Tag()
	}
	if selector == "" {
		return "", errors.New("published image must contain a tag or immutable digest")
	}
	client, err := azcontainerregistry.NewClient("https://"+host, r.Credential, &azcontainerregistry.ClientOptions{
		ClientOptions: azcore.ClientOptions{Transport: registryTransport{host: host, next: r.Transport}},
	})
	if err != nil {
		return "", err
	}
	fetch := func(selector string) (string, error) {
		accept := "application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.docker.distribution.manifest.list.v2+json"
		response, err := client.GetManifest(ctx, repository, selector, &azcontainerregistry.ClientGetManifestOptions{Accept: &accept})
		if err != nil {
			return "", errors.New("cannot read published ACR manifest with azd credentials")
		}
		if response.ManifestData == nil {
			return "", errors.New("ACR manifest response is empty")
		}
		bytes, err := io.ReadAll(io.LimitReader(response.ManifestData, maxBody+1))
		closeErr := response.ManifestData.Close()
		if err != nil || closeErr != nil || len(bytes) > maxBody {
			return "", errors.New("cannot read bounded ACR manifest")
		}
		sum := sha256.Sum256(bytes)
		actual := "sha256:" + hex.EncodeToString(sum[:])
		if response.DockerContentDigest == nil || *response.DockerContentDigest != actual {
			return "", errors.New("ACR manifest digest/header verification failed")
		}
		if expectedConfig != "" {
			var manifest struct {
				Config struct {
					Digest string `json:"digest"`
				} `json:"config"`
			}
			if json.Unmarshal(bytes, &manifest) != nil || manifest.Config.Digest != expectedConfig {
				return "", errors.New("published manifest does not match the packaged image/config digest; tag may have changed (build indexes are not supported)")
			}
		}
		return actual, nil
	}
	digest, err := fetch(selector)
	if err != nil {
		return "", err
	}
	verified, err := fetch(digest)
	if err != nil || verified != digest {
		return "", errors.New("published immutable ACR manifest readback failed")
	}
	result := host + "/" + repository + "@" + digest
	if _, ok := parsed.(reference.Digested); ok && result != image {
		return "", errors.New("reference image differs from verified digest")
	}
	if err := config.ValidateImage(result); err != nil {
		return "", err
	}
	return result, nil
}
