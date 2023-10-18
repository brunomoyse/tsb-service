<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductTagTranslation;
use Illuminate\Database\Seeder;

class ProductSashimiSeeder extends Seeder
{
    public function run()
    {
        $productTag = ProductTagTranslation::query()
            ->where('locale', 'FR')
            ->where('name', 'Sashimi')
            ->firstOrFail()->product_tag_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Saumon',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon',
                        ],
                    ],
                ],
                'price' => 8.60,
                'code' => 'I1',
                'is_active' => true,
                'slug' => 'sashimi-saumon',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Thon',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tuna',
                        ],
                    ],
                ],
                'price' => 10.80,
                'code' => 'I2',
                'is_active' => true,
                'slug' => 'sashimi-thon',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Saumon thon',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon tuna',
                        ],
                    ],
                ],
                'price' => 17.80,
                'code' => 'I3',
                'is_active' => true,
                'slug' => 'sashimi-saumon-thon',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Assortiment',
                            'description' => 'Saumon, thon, dorade'
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Mix',
                            'description' => 'Salmon, tuna, sea bream'
                        ],
                    ],
                ],
                'price' => 20.80,
                'code' => 'I4',
                'is_active' => true,
                'slug' => 'sashimi-assortiment',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
        ];

        if (Product::query()->where('slug', 'sashimi-saumon')->exists()) {
            return;
        }

        foreach ($products as $product) {
            try {
                /* @var Product $productItem */
                $productItem = Product::query()->create([
                    'price' => $product['price'],
                    'is_active' => true,
                    'slug' => $product['slug'],
                    'code' => $product['code'] ?? null,
                ]);

                $productItem->productTranslations()->createMany($product['productTranslations']['create']);
                $productItem->productTags()->sync($product['productTags']['connect']);
            } catch (\Exception $e) {
                throw new \Exception('Error creating product: '.$e->getMessage());
            }
        }
    }
}
