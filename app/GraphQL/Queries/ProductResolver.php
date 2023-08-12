<?php

namespace App\GraphQL\Queries;

use App\Models\Product;
use GraphQL\Type\Definition\ResolveInfo;
use Illuminate\Support\Facades\DB;
use Nuwave\Lighthouse\Support\Contracts\GraphQLContext;

class ProductResolver
{
    public function all(null $rootValue, array $args, GraphQLContext $context, ResolveInfo $resolveInfo): array
    {
        try {
            $queryBuilder = Product::query()
                ->with(['productTags', 'productTranslations']);

            // Filter products by tags
            if (isset($args['tags'])) {
                $queryBuilder->whereHas('productTags', function ($query) use ($args) {
                    $query->whereIn('id', $args['tags']);
                });
            }

            // Filter products by name in productTranslations
            if (isset($args['search'])) {
                $queryBuilder->whereHas('productTranslations', function ($query) use ($args) {
                    $query
                        ->where('name', 'ilike', '%'.$args['search'].'%')
                        ->where('language', $args['lang']);
                });
            }

            /** @phpstan-ignore-next-line  */
            $queryBuilder->orderBy(DB::raw("(SELECT name FROM product_translations WHERE products.id = product_translations.product_id AND product_translations.language = '".strtoupper($args['lang'])."' LIMIT 1)"), 'ASC');

            $perPage = $args['first'] ?? 10;
            $page = $args['page'] ?? 1;

            $paginator = $queryBuilder->paginate($perPage, ['*'], 'page', $page);

            return [
                'data' => $paginator->items(),
                'paginatorInfo' => [
                    'count' => $paginator->count(),
                    'currentPage' => $paginator->currentPage(),
                    'firstItem' => $paginator->firstItem(),
                    'hasMorePages' => $paginator->hasMorePages(),
                    'lastItem' => $paginator->lastItem(),
                    'lastPage' => $paginator->lastPage(),
                    'perPage' => $paginator->perPage(),
                    'total' => $paginator->total(),
                ],
            ];

        } catch (\Exception $e) {
            throw new \Exception('Error trying to fetch products: '.$e->getMessage());
        }
    }
}
