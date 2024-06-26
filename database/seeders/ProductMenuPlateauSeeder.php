<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductCategoryTranslation;
use Illuminate\Database\Seeder;

class ProductMenuPlateauSeeder extends Seeder
{
    public function run()
    {
        $productCategory = ProductCategoryTranslation::query()
            ->where('locale', 'fr')
            ->where('name', 'Menu plateau')
            ->firstOrFail()->product_category_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'M1',
                            'description' => '2 sushis saumon, 2 sushis thon, 6 California avocat et 6 makis tempura crevettes.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'M1',
                            'description' => '2 salmon sushis, 2 tuna sushis, 6 avocado California rolls and 6 shrimp tempura makis.',
                        ],
                    ],
                ],
                'price' => 17.90,
                'is_active' => true,
                'slug' => 'menu-m1',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'M2',
                            'description' => '2 brochettes de poulet, 6 California saumon avocat et 6 California tempura crevettes avocat. Servi avec 1 soupe miso.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'M2',
                            'description' => '2 chicken skewers, 6 salmon avocado California rolls and 6 shrimp tempura avocado California rolls. Served with 1 miso soup.',
                        ],
                    ],
                ],
                'price' => 18.80,
                'is_active' => true,
                'slug' => 'menu-m2',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Plateau saumon',
                            'description' => '4 sushi saumon, 6 makis saumon, 6 California saumon avocat et 6 springs rolls saumon avocat.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Salmon platter',
                            'description' => '4 salmon sushi, 6 salmon makis, 6 salmon avocado California rolls, and 6 salmon avocado spring rolls.',
                        ],
                    ],
                ],
                'price' => 21.80,
                'is_active' => true,
                'slug' => 'menu-plateau-saumon',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Plateau California 24',
                            'description' => '6 California rolls saumon avocat, 6 California rolls thon avocat, 6 California rolls saumon mangue et 6 California rolls tempura crevettes avocat.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'California platter 24',
                            'description' => '6 salmon avocado California rolls, 6 tuna avocado California rolls, 6 salmon mango California rolls, and 6 shrimp tempura avocado California rolls.',
                        ],
                    ],
                ],
                'price' => 24.80,
                'is_active' => true,
                'slug' => 'menu-plateau-california-24',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Plateau 46',
                            'description' => '2 sushi saumon, 2 sushi thon, 6 california tempura crevette avocat cheese fraise, 6 california saumon avocat, 6 california saumon mangue, 6 california thon cuit pomme spicy, 6 spring poulet mangue menthe, 6 saumon roll cheese, 6 maki avocat.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Platter 46',
                            'description' => '2 salmon sushi, 2 tuna sushi, 6 shrimp tempura avocado strawberry cheese California rolls, 6 salmon avocado California rolls, 6 salmon mango California rolls, 6 cooked tuna apple spicy California rolls, 6 chicken mango mint spring rolls, 6 salmon cheese rolls, 6 avocado makis.',
                        ],
                    ],
                ],
                'price' => 50.80,
                'is_active' => true,
                'slug' => 'menu-plateau-46',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Plateau 36',
                            'description' => '2 sushi saumon, 2 sushi crevettes, 2 sushi thon, 2 sushi anguille, 2 sashimi saumon, 2 sashimi thon, 6 makis thon cuit spicy, 6 California rolls saumon mangue, 6 California rolls saumon cheese et 6 masago rolls tempura crevettes avocat.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Platter 36',
                            'description' => '2 salmon sushi, 2 shrimp sushi, 2 tuna sushi, 2 eel sushi, 2 salmon sashimi, 2 tuna sashimi, 6 spicy cooked tuna makis, 6 salmon mango California rolls, 6 salmon cheese California rolls, and 6 masago tempura shrimp avocado rolls.',
                        ],
                    ],
                ],
                'price' => 42.80,
                'is_active' => true,
                'slug' => 'menu-plateau-36',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'M4',
                            'description' => '1 brochette de poulet, 1 brochette de bœuf au fromage, 6 ravioli japonais, 6 California saumon avocat, 1 sushis saumon,1 sushis crevettes, 1sushis avocat et 1sushis omelette. Servi avec 1 soupe au choix.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'M4',
                            'description' => '1 chicken skewer, 1 beef with cheese skewer, 6 Japanese ravioli, 6 salmon avocado California rolls, 1 salmon sushi, 1 shrimp sushi, 1 avocado sushi, and 1 omelette sushi. Served with a soup of your choice.',
                        ],
                    ],
                ],
                'price' => 26.80,
                'is_active' => true,
                'slug' => 'menu-m4',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Plateau sushi mix',
                            'description' => '2 sushi saumon, 2 sushi thon, 2 sushi crevettes, 2 sushi saumon cuit caramélisé, 1 gunkan œufs saumon, 1 sushi octopus, 1 sushi tempura crevettes et 1 sushi omelette.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Mixed sushi platter',
                            'description' => '2 salmon sushi, 2 tuna sushi, 2 shrimp sushi, 2 caramelized cooked salmon sushi, 1 salmon egg gunkan, 1 octopus sushi, 1 shrimp tempura sushi, and 1 omelette sushi.',
                        ],
                    ],
                ],
                'price' => 24.80,
                'is_active' => true,
                'slug' => 'menu-plateau-sushi-mix',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Plateau 38 Pièces',
                            'description' => '2 sushi saumon cuit caramélisé, 2 sushi daurade grillé, 2 sushi crevettes, 6 springs tempura crevettes oignons frits, 6 California poulet roquette miel, 6 California thon cuit pomme spicy, 6 California tempura crevettes avocat et 8 oignons rolls.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => '38 Piece Platter',
                            'description' => '2 caramelized cooked salmon sushi, 2 grilled bream sushi, 2 shrimp sushi, 6 tempura shrimp spring rolls with fried onions, 6 chicken arugula honey California rolls, 6 cooked tuna spicy apple California rolls, 6 tempura shrimp avocado California rolls, and 8 onion rolls.',
                        ],
                    ],
                ],
                'price' => 49.80,
                'is_active' => true,
                'slug' => 'menu-plateau-38',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saumon Time',
                            'description' => '4 sushi saumon, 6 California saumon avocat et 6 California saumon cheese.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Salmon Time',
                            'description' => '4 salmon sushi, 6 salmon avocado California rolls, and 6 salmon cheese California rolls.',
                        ],
                    ],
                ],
                'price' => 17.00,
                'is_active' => true,
                'slug' => 'menu-plateau-saumon-time',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Plateau 42 Pièces',
                            'description' => '2 sushi saumon, 2 sushi thon, 2 sushi daurade grillées, 2 tulipes saumon avocat, 2 sashimi saumon, 2 sashimi thon, 3 makis saumon, 3 makis thon, 6 California saumon avocat, 6 California poulet roquette miel, 6 oignons frits foie gras miel et 6 springs rolls tempura crevettes oignons.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => '42 Piece Platter',
                            'description' => '2 salmon sushi, 2 tuna sushi, 2 grilled bream sushi, 2 salmon avocado tulips, 2 salmon sashimi, 2 tuna sashimi, 3 salmon makis, 3 tuna makis, 6 salmon avocado California rolls, 6 chicken arugula honey California rolls, 6 fried onions foie gras honey, and 6 tempura shrimp onion spring rolls.',
                        ],
                    ],
                ],
                'price' => 52.50,
                'is_active' => true,
                'slug' => 'menu-plateau-42',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'M3',
                            'description' => '4 ravioli japonais, 2 sushis saumon, 2 sushis crevettes, 2 sushis thon, 2 sushis daurade et 2 sushis avocat.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'M3',
                            'description' => '4 Japanese ravioli, 2 salmon sushi, 2 shrimp sushi, 2 tuna sushi, 2 bream sushi and 2 avocado sushi.',
                        ],
                    ],
                ],
                'price' => 21.80,
                'is_active' => true,
                'slug' => 'menu-m3',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'M7',
                            'description' => '4 tempura crevettes, 6 makis saumon, 6 makis thon, 2 sushi thon, 2 sushi saumon, 2 sushi avocat, 2 sushi daurade grillé, 8 miel rolls, 6 tempura crevettes cheese et 4 sashimi saumon. Servi avec 2 soupes au choix.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'M7',
                            'description' => '4 shrimp tempura, 6 salmon makis, 6 tuna makis, 2 tuna sushi, 2 salmon sushi, 2 avocado sushi, 2 grilled bream sushi, 8 honey rolls, 6 tempura shrimp cheese, and 4 salmon sashimi. Served with 2 soups of your choice.',
                        ],
                    ],
                ],
                'price' => 56.80,
                'is_active' => true,
                'slug' => 'menu-m7',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'M5',
                            'description' => '4 ravioli japonais, 2 sushis saumon, 2 sushis thon, 3 makis thon, 3 makis concombres, 6 California saumon avocat, 2 sashimi saumon et 2 sashimi thon. Servi avec 1 soupe au choix.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'M5',
                            'description' => '4 Japanese ravioli, 2 salmon sushi, 2 tuna sushi, 3 tuna makis, 3 cucumber makis, 6 salmon avocado California rolls, 2 salmon sashimi, and 2 tuna sashimi. Served with 1 soup of your choice.',
                        ],
                    ],
                ],
                'price' => 30.80,
                'is_active' => true,
                'slug' => 'menu-m5',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Sushi saumon',
                            'description' => '1 sushi saumon, 1 sushi saumon mi-cuit, 1 sushi saumon mi-cuit caramélisé, 1 sushi saumon cheese, 1 sushi saumon avocat et 1 sushi saumon mangue.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Salmon sushi',
                            'description' => '1 salmon sushi, 1 half-cooked salmon sushi, 1 caramelized half-cooked salmon sushi, 1 salmon cheese sushi, 1 salmon avocado sushi, and 1 salmon mango sushi.',
                        ],
                    ],
                ],
                'price' => 14.80,
                'is_active' => true,
                'slug' => 'menu-plateau-sushi-saumon',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Plateau 16',
                            'description' => '6 California saumon avocat, 6 makis thon, 1 sushi saumon, 1 sushi crevettes, 1 sushi thon et 1 sushi daurade.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Platter 16',
                            'description' => '6 salmon avocado California rolls, 6 tuna makis, 1 salmon sushi, 1 shrimp sushi, 1 tuna sushi, and 1 bream sushi.',
                        ],
                    ],
                ],
                'price' => 18.50,
                'is_active' => true,
                'slug' => 'menu-plateau-16',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Plateau Tokyo',
                            'description' => '16 ravioli japonais, 8 miel rolls, 4 sushi saumon, 4 sushi crevettes, 4 sushi thon, 6 makis saumon, 6 makis thon, 6 spring rolls thon avocat, 6 California saumon avocat, 6 California saumon mangue, 4 sashimi saumon et 4 sashimi thon. Servi avec 4 soupes au choix.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Platter Menu',
                            'description' => '16 Japanese ravioli, 8 honey rolls, 4 salmon sushi, 4 shrimp sushi, 4 tuna sushi, 6 salmon makis, 6 tuna makis, 6 tuna avocado spring rolls, 6 salmon avocado California rolls, 6 salmon mango California rolls, 4 salmon sashimi, and 4 tuna sashimi. Served with 4 soups of your choice.',
                        ],
                    ],
                ],
                'price' => 98.00,
                'is_active' => true,
                'slug' => 'menu-plateau-tokyo',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Plateau 80',
                            'description' => '4 sushi saumon, 4 sushi thon, 4 sushi daurade, 4 sushi avocat, 6 makis saumon, 6 makis foie gras, 6 California saumon avocat, 6 California tempura crevettes avocat, 6 oignons frits poulet, 6 springs rolls thon avocat, 6 springs rolls saumon mangue, 6 crevettes avocat cheese, 8 miel rolls, 4 sashimi saumon et 4 sashimi thon.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Platter 80',
                            'description' => '4 salmon sushi, 4 tuna sushi, 4 bream sushi, 4 avocado sushi, 6 salmon makis, 6 foie gras makis, 6 salmon avocado California rolls, 6 shrimp tempura avocado California rolls, 6 fried onion chicken, 6 tuna avocado spring rolls, 6 salmon mango spring rolls, 6 shrimp avocado cheese, 8 honey rolls, 4 salmon sashimi, and 4 tuna sashimi.',
                        ],
                    ],
                ],
                'price' => 92.80,
                'is_active' => true,
                'slug' => 'menu-plateau-80',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Plateau sushi grillé',
                            'description' => '3 sushi saumon, 3 sushi daurade, 2 sushi Saint-Jacques, 1 sushi saumon caramélisé et 1 sushi foie gras.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Grilled Sushi Platter',
                            'description' => '3 salmon sushi, 3 bream sushi, 2 Saint-Jacques sushi, 1 caramelized salmon sushi, and 1 foie gras sushi.',
                        ],
                    ],
                ],
                'price' => 24.80,
                'is_active' => true,
                'slug' => 'menu-plateau-sushi-grille',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'M6',
                            'description' => '2 brochettes de bœuf au fromage, 2 brochettes de poulet, 4 tempura crevettes, 4 ravioli japonais, 8 oignons rolls, 6 makis thon, 6 California saumon avocat, 6 springs rolls thon avocat et 4 sashimis saumon. Servi avec 2 soupes au choix.',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'M6',
                            'description' => '2 beef cheese skewers, 2 chicken skewers, 4 shrimp tempura, 4 Japanese ravioli, 8 onion rolls, 6 tuna makis, 6 salmon avocado California rolls, 6 tuna avocado spring rolls, and 4 salmon sashimi. Served with 2 soups of your choice.',
                        ],
                    ],
                ],
                'price' => 50.80,
                'is_active' => true,
                'slug' => 'menu-m6',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
        ];

        if (Product::query()->where('slug', 'menu-m1')->exists()) {
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
