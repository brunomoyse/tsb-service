<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductTagTranslation;
use Illuminate\Database\Seeder;

class ProductMasagoRollSeeder extends Seeder
{
    public function run()
    {
        $productTag = ProductTagTranslation::query()
            ->where('locale', 'FR')
            ->where('name', 'Masago roll')
            ->firstOrFail()->product_tag_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Saumon avocat',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon avocado',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'E11',
                'is_active' => true,
                'slug' => 'masago-roll-saumon-avocat',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Thon mangue',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tuna mango',
                        ],
                    ],
                ],
                'price' => 6.50,
                'code' => 'E12',
                'is_active' => true,
                'slug' => 'masago-roll-thon-mangue',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Tempura crevette',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Shrimp tempura',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'E13',
                'is_active' => true,
                'slug' => 'masago-roll-tempura-crevette',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
        ];

        if (Product::query()->where('slug', 'masago-roll-saumon-avocat')->exists()) {
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
