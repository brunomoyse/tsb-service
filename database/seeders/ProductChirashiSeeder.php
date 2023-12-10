<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductTagTranslation;
use Illuminate\Database\Seeder;

class ProductChirashiSeeder extends Seeder
{
    public function run()
    {
        $productTag = ProductTagTranslation::query()
            ->where('locale', 'FR')
            ->where('name', 'Chirashi')
            ->firstOrFail()->product_tag_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Tranches (ou tartare) de saumon avocat',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Slices (or tartare) of salmon and avocado',
                        ],
                    ],
                ],
                'price' => 14.80,
                'code' => 'H1',
                'is_active' => true,
                'slug' => 'chirashi-saumon-avocat',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Tranches (ou tartare) de thon avocat',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Slices (or tartare) of tuna and avocado',
                        ],
                    ],
                ],
                'price' => 15.80,
                'code' => 'H2',
                'is_active' => true,
                'slug' => 'chirashi-thon-avocat',
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
                            'description' => 'Saumon, thon, dorade, crevette, oeufs de saumon, avocat, radis japonais',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Mix',
                            'description' => 'Salmon, tuna, sea bream, shrimp, salmon eggs, avocado, Japanese radish.',
                        ],
                    ],
                ],
                'price' => 17.80,
                'code' => 'H4',
                'is_active' => true,
                'slug' => 'chirashi-assortiment',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
        ];

        if (Product::query()->where('slug', 'chirashi-saumon-avocat')->exists()) {
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
