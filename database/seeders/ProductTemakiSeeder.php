<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductTagTranslation;
use Illuminate\Database\Seeder;

class ProductTemakiSeeder extends Seeder
{
    public function run()
    {
        $productTag = ProductTagTranslation::query()
            ->where('locale', 'FR')
            ->where('name', 'Temaki')
            ->firstOrFail()->product_tag_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Saumon avocat cocombre',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon avocado cucumber',
                        ],
                    ],
                ],
                'price' => 4.50,
                'code' => 'F1',
                'is_active' => true,
                'slug' => 'temaki-saumon-avocat-concombre',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Thon avocat concombre',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tuca avocado cucumber',
                        ],
                    ],
                ],
                'price' => 4.90,
                'code' => 'F2',
                'is_active' => true,
                'slug' => 'temaki-thon-avocat-concombre',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Tempura crevette avocat concombre',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Shrimp tempura avocado cucumber',
                        ],
                    ],
                ],
                'price' => 4.80,
                'code' => 'F3',
                'is_active' => true,
                'slug' => 'temaki-tempura-crevette-avocat-concombre',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Oeufs de saumon avocat concombre',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon eggs avocado cucumber',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'F4',
                'is_active' => true,
                'slug' => 'temaki-oeufs-de-saumon-avocat-concombre',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
        ];

        if (Product::query()->where('slug', 'temaki-saumon-avocat-concombre')->exists()) {
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
