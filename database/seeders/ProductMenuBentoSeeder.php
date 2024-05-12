<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductCategoryTranslation;
use Illuminate\Database\Seeder;

class ProductMenuBentoSeeder extends Seeder
{
    public function run()
    {
        $productCategory = ProductCategoryTranslation::query()
            ->where('locale', 'fr')
            ->where('name', 'Menu bento box')
            ->firstOrFail()->product_category_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Bento box 1',
                            'description' => '2 brochettes de poulet + 3 raviolis japonais au poulet + poulet croustillant + riz blanc + salade de chou + 6 california oignons frits poulet.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Bento box 1',
                            'description' => '2 chicken skewers + 3 Japanese chicken ravioli + crispy chicken + white rice + coleslaw + 6 California fried onion chicken.',
                        ],
                    ],
                ],
                'price' => 17.80,
                'is_active' => true,
                'slug' => 'menu-bento-box-1',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Bento box 2',
                            'description' => '2 tempura crevette + 8 oignons saumon avocat + saumon grillé + riz blanc + salade maison.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Bento box 2',
                            'description' => '2 shrimp tempura + 8 onions salmon avocado + grilled salmon + white rice + homemade salad.',
                        ],
                    ],
                ],
                'price' => 18.80,
                'is_active' => true,
                'slug' => 'menu-bento-box-2',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Bento royal',
                            'description' => 'Riz vinaigré, tartare de saumon avocat et mangue, 12 pièces de sushi du chef (saumon mangue mayo japonais, crispy saumon mayo spicy, poulet sucré halal), petite salade wakame et choux.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Royal bento',
                            'description' => "Vinegar rice, salmon, avocado and mango tartare, 12 pieces of chef's sushi (Japanese salmon mango mayo, crispy spicy mayo salmon, sweet halal chicken), small wakame and cabbage salad..",
                        ],
                    ],
                ],
                'price' => 18.80,
                'is_active' => true,
                'slug' => 'menu-bento-royal',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Bento végétarien',
                            'description' => 'Riz vinaigré, tartare de saumon avocat et mangue, 12 pièces de sushi du chef (saumon mangue mayo japonais, crispy saumon mayo spicy, poulet sucré halal), petite salade wakame et choux.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Vegetarian bento',
                            'description' => "Vinegar rice, salmon, avocado and mango tartare, 12 pieces of chef's sushi (Japanese salmon mango mayo, crispy spicy mayo salmon, sweet halal chicken), small wakame and cabbage salad..",
                        ],
                    ],
                ],
                'price' => 15.80,
                'is_active' => true,
                'slug' => 'menu-bento-vegetarien',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
        ];

        if (Product::query()->where('slug', 'menu-bento-box-1')->exists()) {
            return;
        }

        foreach ($products as $product) {
            try {
                /* @var Product $productItem */
                $productItem = Product::query()->create([
                    'price' => $product['price'],
                    'is_active' => true,
                    'slug' => $product['slug'],
                ]);

                $productItem->productTranslations()->createMany($product['productTranslations']['create']);
                $productItem->productCategories()->sync($product['productCategories']['connect']);
            } catch (\Exception $e) {
                throw new \Exception('Error creating product: '.$e->getMessage());
            }
        }
    }
}
