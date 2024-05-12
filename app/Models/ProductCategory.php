<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Concerns\HasUuids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsToMany;
use Illuminate\Database\Eloquent\Relations\HasMany;

class ProductCategory extends Model
{
    use HasUuids;

    protected $table = 'product_categories';

    public function products(): BelongsToMany
    {
        return $this->belongsToMany(Product::class);
    }

    public function productCategoryTranslations(): HasMany
    {
        return $this->hasMany(ProductCategoryTranslation::class);
    }
}
