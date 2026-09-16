package cache

// 键空间。凭据与解析结果永不入缓存，故此处没有对应键。
const (
	prefix = "msr:"
)

func CollectionKey(name string) string { return prefix + "collection:" + name }

func UserModelKey(name string) string { return prefix + "usermodel:" + name }

func PolicyKey(name string) string { return prefix + "policy:" + name }

func CatalogKey() string { return prefix + "catalog" }
