<?php

namespace Database\Seeders;

use App\Models\ProductTag;
use App\Models\ProductTagTranslation;
use Illuminate\Database\Seeder;

class TagSeeder extends Seeder
{
    public function run(): void
    {
        $tags = [
            [
                'EN' => 'Platter menu',
                'FR' => 'Menu plateau',

            ],
            [
                'EN' => 'Bento box menu',
                'FR' => 'Menu bento box',
            ],
            [
                'EN' => 'Sushi',
                'FR' => 'Sushi',
            ],
            [
                'EN' => 'Maki',
                'FR' => 'Maki',
            ],
            [
                'EN' => 'Gunkan',
                'FR' => 'Gunkan',
            ],
            [
                'EN' => 'Spring roll',
                'FR' => 'Spring roll',
            ],
            [
                'EN' => 'California roll',
                'FR' => 'California roll',
            ],
            [
                'EN' => 'Temaki',
                'FR' => 'Temaki',
            ],
            [
                'EN' => 'Masago roll',
                'FR' => 'Masago roll',
            ],
            [
                'EN' => 'Special roll',
                'FR' => 'Spécial roll',
            ],
            [
                'EN' => 'Chirashi',
                'FR' => 'Chirashi',
            ],
            [
                'EN' => 'Sashimi',
                'FR' => 'Sashimi',
            ],
            [
                'EN' => 'Poke bowl',
                'FR' => 'Poke bowl',
            ],
            [
                'EN' => 'Tokyo hot',
                'FR' => 'Tokyo hot',
            ],
            [
                'EN' => 'Teppanyaki',
                'FR' => 'Teppanyaki',
            ],
            [
                'EN' => 'Side dish',
                'FR' => 'Accompagnement',
            ],
            [
                'EN' => 'Drink',
                'FR' => 'Boisson',
            ],
        ];

        $index = 1;
        foreach ($tags as $translations) {
            $exists = false;
            foreach ($translations as $locale => $translation) {
                // Check if a tag with the specific translation already exists
                if (ProductTagTranslation::query()->where([
                    ['locale', '=', $locale],
                    ['name', '=', $translation],
                ])->exists()) {
                    $exists = true;
                    break;  // Break the inner loop if any translation exists
                }
            }

            if (! $exists) {
                /** @var ProductTag $productTag */
                $productTag = ProductTag::query()->create([
                    'order' => $index,
                ]);
                $transData = [];
                foreach ($translations as $locale => $translation) {
                    $transData[] = [
                        'locale' => $locale,
                        'name' => $translation,
                    ];
                }
                $productTag->productTagTranslations()->createMany($transData);
                $index++;
            }
        }
    }
}
