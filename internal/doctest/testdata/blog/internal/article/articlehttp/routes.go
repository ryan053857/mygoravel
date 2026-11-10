// Package articlehttp 只声明 Register 的签名，完整代码见路由章节。
package articlehttp

import (
	"github.com/danielgtaylor/huma/v2"

	"example.com/blog/internal/article"
	"github.com/ryan053857/mygoravel/httpx/rest"
)

func Register(api *rest.API, requireArticles func(*huma.Operation), articles *article.Service) {}
