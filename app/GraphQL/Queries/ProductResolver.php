<?php

namespace App\GraphQL\Queries;

use App\Models\Product;
use GraphQL\Type\Definition\ResolveInfo;
use Illuminate\Contracts\Pagination\LengthAwarePaginator;
use Illuminate\Support\Facades\DB;
use Nuwave\Lighthouse\Support\Contracts\GraphQLContext;

class ProductResolver
{
    public function all(null $rootValue, array $args, GraphQLContext $context, ResolveInfo $resolveInfo): LengthAwarePaginator
    {
        try {
            $queryBuilder = Product::query()
                ->with(['productCategories', 'productTranslations', 'preview']);

            // Filter products by categories
            if (isset($args['categories'])) {
                $queryBuilder->whereHas('productCategories', function ($query) use ($args) {
                    $query->whereIn('id', $args['categories']);
                });
            }

            // Filter products by name in productTranslations
            if (isset($args['search'])) {
                $queryBuilder->whereHas('productTranslations', function ($query) use ($args) {
                    $query
                        ->where('name', 'ilike', '%'.$args['search'].'%')
                        ->orWhere('code', 'ilike', '%'.$args['search'].'%')
                        ->where('locale', $args['locale']);
                });
            }

            /** @phpstan-ignore-next-line */
            $queryBuilder->orderByRaw("substring(code from '^[A-Za-z]+')::text, (substring(code from '[0-9]+'))::int ASC");

            /** @phpstan-ignore-next-line  */
            $queryBuilder->orderBy(DB::raw("(SELECT name FROM product_translations WHERE products.id = product_translations.product_id AND product_translations.locale = '".strtoupper($args['locale'])."' LIMIT 1)"), 'ASC');

            return $queryBuilder->paginate($args['first'] ?? config('lighthouse.pagination.default_count'), ['*'], 'page', $args['page'] ?? null);
        } catch (\Exception $e) {
            throw new \Exception('Error trying to fetch products: '.$e->getMessage());
        }
    }

    public function find(null $rootValue, array $args, GraphQLContext $context, ResolveInfo $resolveInfo): Product
    {
        try {
            $queryBuilder = Product::query()
                ->with(['productCategories', 'productTranslations', 'preview']);

            //dd($queryBuilder->findOrFail($args['id'])->toArray());
            /** @var Product */
            return $queryBuilder->findOrFail($args['id']);
        } catch (\Exception $e) {
            throw new \Exception('Error trying to fetch product: '.$e->getMessage());
        }
    }
}
