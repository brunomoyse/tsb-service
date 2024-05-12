<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductCategoryTranslation;
use Illuminate\Database\Seeder;

class ProductSashimiSeeder extends Seeder
{
    public function run()
    {
        $productCategory = ProductCategoryTranslation::query()
            ->where('locale', 'fr')
            ->where('name', 'Sashimi')
            ->firstOrFail()->product_category_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saumon',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Salmon',
                        ],
                    ],
                ],
                'price' => 8.60,
                'code' => 'I1',
                'is_active' => true,
                'slug' => 'sashimi-saumon',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Thon',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Tuna',
                        ],
                    ],
                ],
                'price' => 10.80,
                'code' => 'I2',
                'is_active' => true,
                'slug' => 'sashimi-thon',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saumon thon',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Salmon tuna',
                        ],
                    ],
                ],
                'price' => 17.80,
                'code' => 'I3',
                'is_active' => true,
                'slug' => 'sashimi-saumon-thon',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Assortiment',
                            'description' => 'Saumon, thon, dorade',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Mix',
                            'description' => 'Salmon, tuna, sea bream',
                        ],
                    ],
                ],
                'price' => 20.80,
                'code' => 'I4',
                'is_active' => true,
                'slug' => 'sashimi-assortiment',
                'productCategories' => [
                    'connect' => [$productCategory],
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
                $productItem->productCategories()->sync($product['productCategories']['connect']);
            } catch (\Exception $e) {
                throw new \Exception('Error creating product: '.$e->getMessage());
            }
        }
    }
}
