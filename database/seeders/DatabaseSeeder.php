<?php

namespace Database\Seeders;

// use Illuminate\Database\Console\Seeds\WithoutModelEvents;
use Illuminate\Database\Seeder;

class DatabaseSeeder extends Seeder
{
    /**
     * Seed the application's database.
     */
    public function run(): void
    {
        $this->call(CategorySeeder::class);
        $this->call(ProductMenuPlateauSeeder::class);
        $this->call(ProductSushiSeeder::class);
        $this->call(ProductMenuBentoSeeder::class);
        $this->call(ProductMakiSeeder::class);
        $this->call(ProductGunkanSeeder::class);
        $this->call(ProductSpringRollSeeder::class);
        $this->call(ProductCaliforniaRollSeeder::class);
        $this->call(ProductTemakiSeeder::class);
        $this->call(ProductMasagoRollSeeder::class);
        $this->call(ProductSpecialRollSeeder::class);
        $this->call(ProductChirashiSeeder::class);
        $this->call(ProductSashimiSeeder::class);
        $this->call(ProductPokeBowlSeeder::class);
        // Tokyo hot
        // Teppanyaki
        // Accompagnement
        // Boisson
    }
}
