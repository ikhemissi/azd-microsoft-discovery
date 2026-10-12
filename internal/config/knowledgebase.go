package config

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var kbName = regexp.MustCompile(`^[A-Za-z0-9-]{1,24}$`)

func parseKnowledgeBase(service *Service, value any, lookup Lookup, deferred bool) error {
	options, ok := value.(map[string]any)
	if !ok {
		return errors.New("knowledgebase config must be an object")
	}
	options = maps.Clone(options)
	if _, exists := options["name"]; !exists {
		options["name"] = service.Name
	}
	if _, exists := options["autoindex"]; !exists {
		options["autoindex"] = true
	}
	for key, env := range map[string]string{"subscriptionId": "AZURE_SUBSCRIPTION_ID", "resourceGroup": "AZURE_RESOURCE_GROUP"} {
		if _, exists := options[key]; exists {
			continue
		}
		if current, exists := lookup(env); exists && current != "" && !variable.MatchString(current) {
			options[key] = current
		} else if deferred {
			options[key] = "${" + env + "}"
		} else {
			return fmt.Errorf("knowledgebase requires selected %s", env)
		}
	}
	service.KnowledgeBase = &KnowledgeBase{}
	if err := decodeMode("knowledgebase", options, lookup, service.KnowledgeBase, deferred); err != nil {
		return err
	}
	kb := service.KnowledgeBase
	if !kbName.MatchString(kb.Name) {
		return errors.New("KB name must be 1-24 alphanumeric/hyphen characters")
	}
	for _, text := range []string{kb.BookshelfName, kb.ResourceGroup} {
		if deferred && variable.FindString(text) == text {
			continue
		}
		if text == "" || text == "." || text == ".." || strings.ContainsAny(text, "/\\?# \r\n\t") {
			return errors.New("invalid Bookshelf name or resource group")
		}
	}
	if !(deferred && variable.FindString(kb.SubscriptionID) == kb.SubscriptionID) {
		id, err := uuid.Parse(kb.SubscriptionID)
		if err != nil || len(kb.SubscriptionID) != 36 || id == uuid.Nil {
			return errors.New("KB subscriptionId must be a nonzero GUID")
		}
	}
	seen := map[string]bool{}
	for _, source := range kb.Sources {
		if deferred && variable.FindString(source.StorageAssetResourceID) == source.StorageAssetResourceID {
			continue
		}
		if _, err := resource(source.StorageAssetResourceID, "Microsoft.Discovery/storageContainers/storageAssets"); err != nil {
			return fmt.Errorf("KB source storageAssetResourceId: %w", err)
		}
		key := strings.ToLower(source.StorageAssetResourceID)
		if seen[key] {
			return errors.New("duplicate KB source")
		}
		seen[key] = true
	}
	if kb.NodePoolResourceID != "" && !(deferred && variable.FindString(kb.NodePoolResourceID) == kb.NodePoolResourceID) {
		if _, err := resource(kb.NodePoolResourceID, "Microsoft.Discovery/supercomputers/nodePools"); err != nil {
			return err
		}
	}
	return nil
}

func BookshelfResourceID(kb *KnowledgeBase) string {
	return "/subscriptions/" + kb.SubscriptionID + "/resourceGroups/" + kb.ResourceGroup +
		"/providers/Microsoft.Discovery/bookshelves/" + kb.BookshelfName
}
