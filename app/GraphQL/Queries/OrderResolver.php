<?php

namespace App\GraphQL\Queries;

use App\Models\Order;
use GraphQL\Type\Definition\ResolveInfo;
use Nuwave\Lighthouse\Support\Contracts\GraphQLContext;

class OrderResolver
{
    public function find(null $rootValue, array $args, GraphQLContext $context, ResolveInfo $resolveInfo): Order
    {
        /** @var Order $product */
        $product = Order::query()
            ->with(['products', 'products.productTranslations', 'user'])
            ->findOrFail($args['id']);

        return $product;
    }
}
