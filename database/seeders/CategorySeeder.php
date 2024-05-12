<?php

namespace Database\Seeders;

use App\Models\ProductCategory;
use App\Models\ProductCategoryTranslation;
use Illuminate\Database\Seeder;

class CategorySeeder extends Seeder
{
    public function run(): void
    {
        $categories = [
            [
                'en' => 'Platter menu',
                'fr' => 'Menu plateau',

            ],
            [
                'en' => 'Bento box menu',
                'fr' => 'Menu bento box',
            ],
            [
                'en' => 'Sushi',
                'fr' => 'Sushi',
            ],
            [
                'en' => 'Maki',
                'fr' => 'Maki',
            ],
            [
                'en' => 'Gunkan',
                'fr' => 'Gunkan',
            ],
            [
                'en' => 'Spring roll',
                'fr' => 'Spring roll',
            ],
            [
                'en' => 'California roll',
                'fr' => 'California roll',
            ],
            [
                'en' => 'Temaki',
                'fr' => 'Temaki',
            ],
            [
                'en' => 'Masago roll',
                'fr' => 'Masago roll',
            ],
            [
                'en' => 'Special roll',
                'fr' => 'Spécial roll',
            ],
            [
                'en' => 'Chirashi',
                'fr' => 'Chirashi',
            ],
            [
                'en' => 'Sashimi',
                'fr' => 'Sashimi',
            ],
            [
                'en' => 'Poke bowl',
                'fr' => 'Poke bowl',
            ],
            [
                'en' => 'Tokyo hot',
                'fr' => 'Tokyo hot',
            ],
            [
                'en' => 'Teppanyaki',
                'fr' => 'Teppanyaki',
            ],
            [
                'en' => 'Side dish',
                'fr' => 'Accompagnement',
            ],
            [
                'en' => 'Drink',
                'fr' => 'Boisson',
            ],
        ];

        $index = 1;
        foreach ($categories as $translations) {
            $exists = false;
            foreach ($translations as $locale => $translation) {
                // Check if a category with the specific translation already exists
                if (ProductCategoryTranslation::query()->where([
                    ['locale', '=', $locale],
                    ['name', '=', $translation],
                ])->exists()) {
                    $exists = true;
                    break;  // Break the inner loop if any translation exists
                }
            }

            if (! $exists) {
                /** @var ProductCategory $productCategory */
                $productCategory = ProductCategory::query()->create([
                    'order' => $index,
                ]);
                $transData = [];
                foreach ($translations as $locale => $translation) {
                    $transData[] = [
                        'locale' => $locale,
                        'name' => $translation,
                    ];
                }
                $productCategory->productCategoryTranslations()->createMany($transData);
                $index++;
            }
        }
    }
}
